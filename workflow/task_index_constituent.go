package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

var TaskUpdateIndexConstituent *Task

func init() {
	TaskUpdateIndexConstituent = &Task{
		Name:      "update_index_constituent",
		DependsOn: []string{"calc_factor"},
		Executor:  executeUpdateIndexConstituent,
		OnError:   ErrorModeSkip,
	}
	registerTask(TaskUpdateIndexConstituent, "update")
}

func executeUpdateIndexConstituent(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("🧩 开始从东方财富下载并更新指数成分股数据")

	indexList := []struct {
		ID   string
		Name string
	}{
		{ID: "930903", Name: "ChinaA"},
		{ID: "000300", Name: "CSI300"},
		{ID: "000905", Name: "CSI500"},
		{ID: "000852", Name: "CSI1000"},
		{ID: "932000", Name: "CSI2000"},
	}

	var allConstituents []model.IndexConstituent

	for _, idx := range indexList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		fmt.Printf("📥 正在下载 %s (%s) 成分股数据...\n", idx.Name, idx.ID)
		
		consts, err := downloadEastMoneyIndex(ctx, idx.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to download index %s: %w", idx.ID, err)
		}

		for i := range consts {
			consts[i].IndexName = idx.Name
		}
		allConstituents = append(allConstituents, consts...)
		fmt.Printf("✅ %s 成分股下载成功，共 %d 条记录\n", idx.Name, len(consts))
	}

	if len(allConstituents) == 0 {
		return &TaskResult{State: StateSkipped, Message: "no constituent data parsed"}, nil
	}

	csvPath := filepath.Join(args.TempDir, "index_constituent.csv")
	writer, err := utils.NewCSVWriter[model.IndexConstituent](csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSV writer: %w", err)
	}

	if err := writer.Write(allConstituents); err != nil {
		writer.Close()
		return nil, err
	}
	writer.Close()

	if err := db.TruncateTable(model.TableIndexConstituent); err != nil {
		return nil, fmt.Errorf("failed to truncate table: %w", err)
	}

	if err := db.ImportCSV(model.TableIndexConstituent, csvPath); err != nil {
		return nil, fmt.Errorf("failed to import CSV to database: %w", err)
	}

	msg := fmt.Sprintf("index constituents updated: %d rows", len(allConstituents))
	fmt.Printf("🚀 指数成分股数据导入成功\n")
	return &TaskResult{State: StateCompleted, Rows: len(allConstituents), Message: msg}, nil
}

func downloadEastMoneyIndex(ctx context.Context, indexID string) ([]model.IndexConstituent, error) {
	url := fmt.Sprintf("https://fundztapi.eastmoney.com/FundSpecialApiNew/FundSpecialZSB30ZSCFG?IndexCode=%s&pageIndex=1&pageSize=10000&deviceid=1234567890&version=6.9.9&product=EFund&plat=Iphone&ServerVersion=6.9.9", indexID)
	
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			fmt.Printf("   ⚠️ 下载东财页面失败，正在重试 (%d/3)...\n", attempt)
			time.Sleep(2 * time.Second)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "*/*")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
			continue
		}

		var apiResp struct {
			Datas []struct {
				StockCode string `json:"StockCode"`
				StockName string `json:"StockName"`
			} `json:"Datas"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			lastErr = err
			continue
		}

		var results []model.IndexConstituent
		seen := make(map[string]bool)

		for _, item := range apiResp.Datas {
			code := item.StockCode
			name := strings.TrimSpace(item.StockName)
			if code == "" {
				continue
			}

			symbol, ok := model.SymbolFromCode(code)
			if !ok {
				// 兜底逻辑
				if strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68") {
					symbol = "sh" + code
				} else if strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30") {
					symbol = "sz" + code
				} else if strings.HasPrefix(code, "43") || strings.HasPrefix(code, "83") ||
					strings.HasPrefix(code, "87") || strings.HasPrefix(code, "88") ||
					strings.HasPrefix(code, "920") {
					symbol = "bj" + code
				} else {
					symbol = "sz" + code
				}
			}

			if model.ClassifyCode(symbol) == "stock" {
				if !seen[symbol] {
					seen[symbol] = true
					results = append(results, model.IndexConstituent{
						Symbol: symbol,
						Name:   name,
					})
				}
			}
		}

		return results, nil
	}

	return nil, fmt.Errorf("failed to download after 3 attempts: %w", lastErr)
}
