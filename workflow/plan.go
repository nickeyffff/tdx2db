package workflow

import (
	"fmt"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

// WorkPlan 在任务图启动前汇总交易日历与各表最新日期，决定哪些任务真正需要跑。
// 任务框架里的 SkipIf 只读取本结构，不再各自重复查询。
// WorkPlan 在任务图启动前汇总交易日历与各表最新日期，决定哪些任务真正需要跑。
// 任务框架里的 SkipIf 只读取本结构，不再各自重复查询。
type WorkPlan struct {
	Today          time.Time
	LastTradingDay time.Time
	Calendar       *TradingCalendar

	DailyLatest   time.Time
	BasicLatest   time.Time
	FactorLatest  time.Time
	Min1MinLatest time.Time
	Min5MinLatest time.Time

	NeedDaily    bool
	NeedGbbq     bool
	NeedBasic    bool
	NeedFactor   bool
	NeedHolidays bool
	Need1Min     bool
	Need5Min     bool

	Reason string // 用于日志
}

// AnyNeeded 是否有任何任务需要执行。
func (p *WorkPlan) AnyNeeded() bool {
	return p.NeedDaily || p.NeedGbbq || p.NeedBasic || p.NeedFactor || p.NeedHolidays || p.Need1Min || p.Need5Min
}

// BuildWorkPlan 读取交易日历与各表最新日期，推导本次 cron 要做什么。
func BuildWorkPlan(db database.DataRepository, today time.Time, min bool) (*WorkPlan, error) {
	holidays, err := db.GetHolidays()
	if err != nil {
		return nil, fmt.Errorf("failed to load holidays: %w", err)
	}

	plan := &WorkPlan{Today: today}

	// raw_holidays 为空：属于首次运行 / 旧库，放行所有任务让其自行写入节假日。
	if len(holidays) == 0 {
		plan.NeedDaily = true
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
		plan.Need1Min = true
		plan.Need5Min = true
		plan.Reason = "🌱 raw_holidays 为空，走完整流程"
		return plan, nil
	}

	cal := NewTradingCalendar(holidays)
	plan.Calendar = cal
	plan.LastTradingDay = cal.LastTradingDayOnOrBefore(today)

	dailyLatest, err := db.GetLatestDate(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest daily date: %w", err)
	}
	basicLatest, err := db.GetLatestDate(model.TableBasicDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest basic date: %w", err)
	}
	factorLatest, err := db.GetLatestDate(model.TableAdjustFactor.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest factor date: %w", err)
	}
	latest1Min, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest 1min date: %w", err)
	}
	latest5Min, err := db.GetLatestDate(model.TableKline5Min.TableName, "datetime")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest 5min date: %w", err)
	}

	plan.DailyLatest = dailyLatest
	plan.BasicLatest = basicLatest
	plan.FactorLatest = factorLatest
	plan.Min1MinLatest = latest1Min
	plan.Min5MinLatest = latest5Min

	// 空库：交给 init 流程；此处不标任何 Need，调用方自行决定。
	if dailyLatest.IsZero() {
		plan.Reason = "🛑 数据库无日线数据，请先运行 init"
		return plan, nil
	}

	plan.NeedDaily = dailyLatest.Before(plan.LastTradingDay)
	// gbbq 与日线同频：日线没新数据时 gbbq 也无须更新。
	plan.NeedGbbq = plan.NeedDaily
	// basic/factor 要追赶 daily；如果 daily 将更新，那之后也必须重算。
	plan.NeedBasic = plan.NeedDaily || basicLatest.Before(dailyLatest)
	plan.NeedFactor = plan.NeedDaily || factorLatest.Before(basicLatest)
	// holidays 来自 gbbq.zip，与 gbbq 同频刷新即可。
	plan.NeedHolidays = plan.NeedGbbq

	if min {
		plan.Need1Min = latest1Min.IsZero() || latest1Min.Before(plan.LastTradingDay)
		plan.Need5Min = plan.Need1Min || latest5Min.IsZero() || latest5Min.Before(latest1Min)
	}

	plan.Reason = describePlan(plan)
	return plan, nil
}

func describePlan(plan *WorkPlan) string {
	dayStr := plan.Today.Format("2006-01-02")
	lastTradingStr := plan.LastTradingDay.Format("2006-01-02")
	cal := plan.Calendar

	// 判定当日是否为休市日
	marketState := "交易日"
	if cal.IsHoliday(plan.Today) {
		marketState = "节假日"
	} else if cal.IsWeekend(plan.Today) {
		marketState = "周末"
	}

	// 格式化各个表的最新时间
	fmtDate := func(t time.Time) string {
		if t.IsZero() {
			return "无数据"
		}
		return t.Format("2006-01-02")
	}

	// 格式化 Need 状态
	fmtDecision := func(need bool, reason string) string {
		if need {
			return fmt.Sprintf("RUN  (%s)", reason)
		}
		return fmt.Sprintf("SKIP (%s)", reason)
	}

	// 计算各个决策的具体原因
	dailyReason := "最新数据已追平交易日"
	if plan.NeedDaily {
		dailyReason = fmt.Sprintf("数据库最新 %s < 最近交易日 %s", fmtDate(plan.DailyLatest), lastTradingStr)
	}

	basicReason := "已追平日线数据"
	if plan.NeedBasic {
		if plan.NeedDaily {
			basicReason = "日线数据即将更新"
		} else {
			basicReason = fmt.Sprintf("最新 %s < 日线最新 %s", fmtDate(plan.BasicLatest), fmtDate(plan.DailyLatest))
		}
	}

	factorReason := "已追平基础指标数据"
	if plan.NeedFactor {
		if plan.NeedDaily {
			factorReason = "日线数据即将更新"
		} else {
			factorReason = fmt.Sprintf("最新 %s < 指标最新 %s", fmtDate(plan.FactorLatest), fmtDate(plan.BasicLatest))
		}
	}

	min1Reason := "未启用分时更新"
	if plan.Need1Min {
		min1Reason = fmt.Sprintf("最新 %s < 最近交易日 %s", fmtDate(plan.Min1MinLatest), lastTradingStr)
	} else if !plan.Min1MinLatest.IsZero() && (plan.Min1MinLatest.Equal(plan.LastTradingDay) || plan.Min1MinLatest.After(plan.LastTradingDay)) {
		min1Reason = "已追平交易日"
	}

	min5Reason := "未启用分时更新"
	if plan.Need5Min {
		if plan.Need1Min {
			min5Reason = "1分钟分时即将更新"
		} else {
			min5Reason = fmt.Sprintf("最新 %s < 1分钟最新 %s", fmtDate(plan.Min5MinLatest), fmtDate(plan.Min1MinLatest))
		}
	} else if !plan.Min5MinLatest.IsZero() && (plan.Min5MinLatest.Equal(plan.Min1MinLatest) || plan.Min5MinLatest.After(plan.Min1MinLatest)) {
		min5Reason = "已追平1分钟分时"
	}

	// 拼装明细报告
	report := fmt.Sprintf(
		"🔍 数据库状态检查明细：\n"+
			"  - 当前时间:   %s (%s)\n"+
			"  - 最近交易日: %s\n"+
			"  - 核心表最新日期:\n"+
			"    * 日线表       (raw_kline_daily):   %s\n"+
			"    * 财务指标表   (raw_basic_daily):   %s\n"+
			"    * 复权因子表   (raw_adjust_factor): %s\n"+
			"    * 1分钟分时表  (raw_kline_1min):    %s\n"+
			"    * 5分钟分时表  (raw_stocks_5min):   %s\n"+
			"  - 任务决策:\n"+
			"    * 日线/除权数据更新 (NeedDaily):    %s\n"+
			"    * 财务基础数据计算 (NeedBasic):    %s\n"+
			"    * 后复权因子计算   (NeedFactor):   %s\n"+
			"    * 1分钟分时拉取    (Need1Min):     %s\n"+
			"    * 5分钟分时合成    (Need5Min):     %s\n"+
			"──────────────────────────────────────────────────\n",
		dayStr, marketState, lastTradingStr,
		fmtDate(plan.DailyLatest), fmtDate(plan.BasicLatest), fmtDate(plan.FactorLatest),
		fmtDate(plan.Min1MinLatest), fmtDate(plan.Min5MinLatest),
		fmtDecision(plan.NeedDaily, dailyReason),
		fmtDecision(plan.NeedBasic, basicReason),
		fmtDecision(plan.NeedFactor, factorReason),
		fmtDecision(plan.Need1Min, min1Reason),
		fmtDecision(plan.Need5Min, min5Reason),
	)

	// 根据具体情况，追加总结信息
	var summary string
	if plan.NeedDaily {
		summary = fmt.Sprintf("📅 数据库日线最新 %s，执行更新", fmtDate(plan.DailyLatest))
	} else if plan.NeedBasic || plan.NeedFactor {
		summary = fmt.Sprintf("📅 日线已是最新 (%s)，补算 basic/factor", fmtDate(plan.DailyLatest))
	} else if plan.Need1Min || plan.Need5Min {
		summary = fmt.Sprintf("📅 日线已是最新 (%s)，更新/合成分时数据", fmtDate(plan.DailyLatest))
	} else {
		switch {
		case cal.IsHoliday(plan.Today):
			summary = fmt.Sprintf("🎉 今日 %s 为节假日，A 股休市，全部任务跳过", dayStr)
		case cal.IsWeekend(plan.Today):
			summary = fmt.Sprintf("🌴 今日 %s 为周末，A 股休市，全部任务跳过", dayStr)
		default:
			summary = fmt.Sprintf("✅ 数据库已是最新 (%s)，全部任务跳过", fmtDate(plan.DailyLatest))
		}
	}

	return report + summary
}
