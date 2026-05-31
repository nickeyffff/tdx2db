# tongdaxin 数据库初始化与本地化配置指引

本文档记录了在 ClickHouse 中初始化 `tongdaxin` 数据库的表、字典及视图的步骤。该方案实现了 **完全本地化**，不跨库调用 `tdx` 数据库，从而保证了数据和权限的独立性。

---

## 整体实施流程

```mermaid
graph TD
    A[创建物理表] --> B[复制/挂载数据]
    B --> C[创建本地字典]
    C --> D[创建 5 分钟复权视图]
```

所有的 SQL 执行请根据具体的权限要求，使用具有对应写权限的账号（如 `admin` 或高权限账号）进行操作。

---

## 💡 ClickHouse 字典表设计原理

在设计前/后复权视图时，我们**没有直接使用传统的 `JOIN` 关联复权因子表**，而是通过创建 **ClickHouse 字典（Dictionaries）** 来实现。以下是这一设计的核心原理和优势：

### 1. 字典表的背景与原理
ClickHouse 字典是一种**常驻内存**的键值对（Key-Value）缓存数据结构，支持高速的主键定位。
* **数据结构**：数据源（如 `raw_adjust_factor` 表）的复权因子会被预先加载到内存中，并通过哈希表（这里使用的是 `COMPLEX_KEY_HASHED` 复合键哈希布局，主键为 `(symbol, date)`）组织。
* **高频读取 (O(1) 复杂度)**：使用内置的 `dictGet` 函数，ClickHouse 可以在数据流经过计算引擎时，以单次接近 O(1) 的速度直接在内存中查询到对应股票在指定日期的复权因子。
* **热更新（LIFETIME）**：字典配置了 `LIFETIME(MIN 0 MAX 3600)`。只要底层物理表 `raw_adjust_factor` 的因子数据被程序重算并更新，字典会在生命周期内自动热加载最新数据，使视图计算立即生效，而不需要重建视图。

### 2. 为什么要使用字典表而不是传统 `JOIN`？
对于 5 分钟级别的高频 K 线数据（往往达到数亿甚至数十亿行级），不使用 `JOIN` 的主要原因如下：

* **避免大规模 Hash Join 导致的 OOM（内存溢出）**：
  如果直接 `JOIN` 复权因子物理表，ClickHouse 在执行查询时需要在内存中对两张大表做哈希关联。这会消耗极其恐怖的 CPU 和内存，导致查询极慢甚至直接报 `Memory limit exceeded` 错误。
* **解决前复权的双重因子计算难题**：
  前复权的计算公式为：`当前价格 * (当天因子 / 最新因子)`。
  如果使用传统 `JOIN`，您必须分别将 5 分钟线数据与“历史因子表”和“最新因子表（经过 GROUP BY 聚合）”做**两次大表 JOIN**，这会导致查询计划异常复杂且性能低下。而利用 `dictGet` 字典查找，只需要直接在表达式中嵌套两次极速的内存检索即可。
* **数据解耦与维护便利**：
  K 线历史数据是静态的（尤其是冷数据），但复权因子（由于分红配股）是经常需要向前回溯重算的。通过字典，物理 K 线数据保持“不复权（BFQ）”静态存储，复权只在视图层动态计算。更新因子数据时，不需要对庞大的 K 线表做任何修改。

---

## 第一步：创建物理表与数据迁移

首先，在 `tongdaxin` 数据库中创建对应的物理表，并从源库 `tdx` 迁移数据。

### 1. 迁移 `raw_stocks_5min` 表（约 3800 万行）
```sql
-- 创建表结构
CREATE TABLE tongdaxin.raw_stocks_5min AS tdx.raw_stocks_5min;

-- 导入数据（流式写入，通常在十几秒内完成）
INSERT INTO tongdaxin.raw_stocks_5min SELECT * FROM tdx.raw_stocks_5min;
```

### 2. 迁移 `cold_stocks_5min` 表（约 3.64 亿行，约 9.08 GB）
由于冷数据数据量庞大，为避免双倍磁盘占用和高内存消耗，推荐使用 **硬链接瞬间挂载** 的方式导入：
```sql
-- 创建表结构
CREATE TABLE tongdaxin.cold_stocks_5min AS tdx.cold_stocks_5min;

-- 使用 REPLACE PARTITION tuple() 自动先清空目标表再挂载数据（秒级瞬间完成，且不占双倍空间）
ALTER TABLE tongdaxin.cold_stocks_5min REPLACE PARTITION tuple() FROM tdx.cold_stocks_5min;
```

### 3. 生成与重算复权因子表 `raw_adjust_factor`
`raw_adjust_factor` 表**不需要从 tdx 库手动迁移**。
该表是本项目的核心计算产物。只要您指向 `tongdaxin` 数据库运行一次 `tdx2db` 程序，工作流（Workflow）就会自动在 `tongdaxin` 库中创建此物理表并完成完整的因子计算和写入：

```shell
# 运行增量与重算任务（程序会自动在 tongdaxin 库中创建并重算因子数据）
tdx2db cron --dburi 'clickhouse://user:password@host:port/tongdaxin'
```
> [!TIP]
> 每次执行 `tdx2db cron` 时，程序内部的 `calc_factor` 任务都会执行 `TRUNCATE` 并重新计算写出该表，因此无需做跨库的数据备份或迁移。


---

## 第二步：创建本地字典

为了实现完全本地化，需要在 `tongdaxin` 下创建字典，并且字典的数据源 `SOURCE` 需使用对 `tongdaxin` 数据库具有 `SELECT` 权限的账号（如只读账号 `tdx_read`）。

### 1. 创建 `tongdaxin.dict_adjust_factor`
```sql
CREATE OR REPLACE DICTIONARY tongdaxin.dict_adjust_factor (
    `symbol` String,
    `date` Date,
    `hfq_factor` Float64 DEFAULT 1.
) 
PRIMARY KEY symbol, date 
SOURCE(CLICKHOUSE(
    HOST '127.0.0.1' 
    PORT 9000 
    USER 'tdx_read'
    PASSWORD 'niwev2nsdf'
    DB 'tongdaxin' 
    TABLE 'raw_adjust_factor'
)) 
LIFETIME(MIN 0 MAX 3600) 
LAYOUT(COMPLEX_KEY_HASHED());
```

### 2. 创建 `tongdaxin.dict_latest_factor`
```sql
CREATE OR REPLACE DICTIONARY tongdaxin.dict_latest_factor (
    `symbol` String,
    `latest_factor` Float64
) 
PRIMARY KEY symbol 
SOURCE(CLICKHOUSE(
    HOST '127.0.0.1' 
    PORT 9000 
    USER 'tdx_read'
    PASSWORD 'niwev2nsdf' 
    DB 'tongdaxin' 
    QUERY 'SELECT symbol, argMax(hfq_factor, date) as latest_factor FROM tongdaxin.raw_adjust_factor GROUP BY symbol'
)) 
LIFETIME(MIN 0 MAX 3600) 
LAYOUT(COMPLEX_KEY_HASHED());
```

---

## 第三步：创建 5 分钟 K 线视图

有了本地表和本地字典后，可以创建三个提供实时复权计算的视图。

### 1. 创建不复权视图 `v_bfq_5min`
```sql
CREATE OR REPLACE VIEW tongdaxin.v_bfq_5min AS 
SELECT symbol, datetime, open, high, low, close, volume, amount 
FROM tongdaxin.raw_stocks_5min 

UNION ALL 

SELECT symbol, toDateTime64(concat(toString(datetime), ''), 0, 'Asia/Shanghai') AS datetime, open, high, low, close, volume, amount 
FROM tongdaxin.cold_stocks_5min;
```

### 2. 创建后复权视图 `v_hfq_5min`
```sql
CREATE OR REPLACE VIEW tongdaxin.v_hfq_5min AS 
SELECT 
    symbol, 
    datetime, 
    round(open * dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))), 2) AS open, 
    round(high * dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))), 2) AS high, 
    round(low * dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))), 2) AS low, 
    round(close * dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))), 2) AS close, 
    volume, 
    amount, 
    dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))) AS hfq_factor 
FROM tongdaxin.v_bfq_5min;
```

### 3. 创建前复权视图 `v_qfq_5min`
```sql
CREATE OR REPLACE VIEW tongdaxin.v_qfq_5min AS 
SELECT 
    symbol, 
    datetime, 
    round(open * (dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))) / dictGet('tongdaxin.dict_latest_factor', 'latest_factor', symbol)), 2) AS open, 
    round(high * (dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))) / dictGet('tongdaxin.dict_latest_factor', 'latest_factor', symbol)), 2) AS high, 
    round(low * (dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))) / dictGet('tongdaxin.dict_latest_factor', 'latest_factor', symbol)), 2) AS low, 
    round(close * (dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))) / dictGet('tongdaxin.dict_latest_factor', 'latest_factor', symbol)), 2) AS close, 
    volume, 
    amount, 
    CAST(dictGet('tongdaxin.dict_adjust_factor', 'hfq_factor', (symbol, toDate(datetime))), 'Float64') AS current_factor, 
    CAST(dictGet('tongdaxin.dict_latest_factor', 'latest_factor', symbol), 'Float64') AS latest_factor 
FROM tongdaxin.v_bfq_5min;
```

---

## 账户与权限参考说明

在 ClickHouse 实例中，不同账号的权限配置有所不同：

* **`trading` 账号**：拥有 `tdx` 数据库下的全套读写和管理权限，但默认无法直接向 `tongdaxin` 数据库写入或读取。
* **`tdx_read` 账号**：拥有对 `tongdaxin` 数据库的 `SHOW` 和 `SELECT` 权限。

因此，如果需要对 `tongdaxin` 库执行 `CREATE TABLE` / `INSERT` 等修改操作，请确保使用管理员账号或已获得 `tongdaxin` 库写权限的高权限账号，而非受限的 `trading` 或只读的 `tdx_read`。
