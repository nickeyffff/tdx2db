package workflow

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakinm/xlsReader/xls"
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
	fmt.Println("🧩 开始下载并更新指数成分股数据")

	indexList := []struct {
		Name string
		File string
	}{
		{Name: "全部A股", File: "930903cons.xls"},
		{Name: "沪深300", File: "000300cons.xls"},
		{Name: "中证500", File: "000905cons.xls"},
		{Name: "中证1000", File: "000852cons.xls"},
		{Name: "中证2000", File: "932000cons.xls"},
	}

	csindexUrl := "https://oss-ch.csindex.com.cn/static/html/csindex/public/uploads/file/autofile/cons/"
	var allConstituents []model.IndexConstituent

	for _, idx := range indexList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		url := csindexUrl + idx.File
		outputPath := filepath.Join(args.TempDir, idx.File)

		fmt.Printf("📥 正在下载 %s 成分数据: %s\n", idx.Name, idx.File)
		status, err := downloadFileWithRetry(ctx, url, outputPath, 3)
		if err != nil {
			return nil, fmt.Errorf("failed to download index file %s (status %d): %w", idx.File, status, err)
		}
		if status == 404 {
			return nil, fmt.Errorf("index file %s not found (HTTP 404)", idx.File)
		}

		fmt.Printf("🔍 正在解析 %s 成分数据\n", idx.Name)
		consts, err := parseXLSFile(outputPath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse index file %s: %w", idx.File, err)
		}

		allConstituents = append(allConstituents, consts...)
		fmt.Printf("✅ %s 成分股解析成功，共 %d 条记录\n", idx.Name, len(consts))

		// Sleep briefly to avoid hitting rate limit
		time.Sleep(1 * time.Second)
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

func downloadFileWithRetry(ctx context.Context, downloadURL, targetPath string, retries int) (int, error) {
	var lastStatus int
	var lastErr error

	for attempt := 1; attempt <= retries; attempt++ {
		if attempt > 1 {
			fmt.Printf("⚠️ 下载失败，正在进行第 %d/%d 次重试...\n", attempt, retries)
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			case <-timer.C:
			}
		}

		status, err := downloadFileDirect(ctx, downloadURL, targetPath)
		if err == nil {
			return status, nil
		}
		lastStatus = status
		lastErr = err
	}

	return lastStatus, fmt.Errorf("failed after %d attempts: %w", retries, lastErr)
}

func downloadFileDirect(ctx context.Context, downloadURL, targetPath string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create GET request: %w", err)
	}

	// Set browser-like headers (modern Chrome SEC headers)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")

	if u, err := url.Parse(downloadURL); err == nil && u.Scheme != "" && u.Host != "" {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute GET request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return 404, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to create output file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to write output file: %w", err)
	}

	return resp.StatusCode, nil
}

func parseXLSFile(filePath string) ([]model.IndexConstituent, error) {
	workbook, err := xls.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open xls file: %w", err)
	}

	sheet, err := workbook.GetSheet(0)
	if err != nil {
		return nil, fmt.Errorf("failed to get first sheet: %w", err)
	}

	numRows := sheet.GetNumberRows()
	if numRows <= 1 {
		return nil, nil
	}

	row0, err := sheet.GetRow(0)
	if err != nil {
		return nil, fmt.Errorf("failed to read header row: %w", err)
	}

	var colIdxIndexName = -1
	var colIdxCode = -1
	var colIdxName = -1
	var colIdxExchange = -1

	// Scan up to 20 columns to locate headers dynamically
	for j := 0; j < 20; j++ {
		cell, err := row0.GetCol(j)
		if err != nil {
			continue
		}
		val := strings.TrimSpace(cell.GetString())
		if val == "" {
			continue
		}
		switch val {
		case "指数英文名称Index Name(Eng)":
			colIdxIndexName = j
		case "成份券代码Constituent Code":
			colIdxCode = j
		case "成份券名称Constituent Name":
			colIdxName = j
		case "交易所英文名称Exchange(Eng)":
			colIdxExchange = j
		}
	}

	if colIdxIndexName == -1 || colIdxCode == -1 || colIdxName == -1 || colIdxExchange == -1 {
		return nil, fmt.Errorf("failed to find required columns: index_name=%d, code=%d, name=%d, exchange=%d",
			colIdxIndexName, colIdxCode, colIdxName, colIdxExchange)
	}

	var results []model.IndexConstituent

	for i := 1; i < numRows; i++ {
		row, err := sheet.GetRow(i)
		if err != nil {
			continue
		}
		if row == nil {
			continue
		}

		var rawIndexName, rawCode, rawName, rawExchange string
		if cell, err := row.GetCol(colIdxIndexName); err == nil {
			rawIndexName = strings.TrimSpace(cell.GetString())
		}
		if cell, err := row.GetCol(colIdxCode); err == nil {
			rawCode = strings.TrimSpace(cell.GetString())
		}
		if cell, err := row.GetCol(colIdxName); err == nil {
			rawName = strings.TrimSpace(cell.GetString())
		}
		if cell, err := row.GetCol(colIdxExchange); err == nil {
			rawExchange = strings.TrimSpace(cell.GetString())
		}

		if rawCode == "" {
			continue
		}

		// Remove spaces in index_name
		indexName := strings.ReplaceAll(rawIndexName, " ", "")

		// Normalize exchange suffix
		var exchange string
		switch rawExchange {
		case "Shenzhen Stock Exchange":
			exchange = "sz"
		case "Shanghai Stock Exchange":
			exchange = "sh"
		case "Beijing Stock Exchange":
			exchange = "bj"
		default:
			lower := strings.ToLower(rawExchange)
			if strings.Contains(lower, "shenzhen") {
				exchange = "sz"
			} else if strings.Contains(lower, "shanghai") {
				exchange = "sh"
			} else if strings.Contains(lower, "beijing") {
				exchange = "bj"
			} else {
				exchange = strings.ToLower(rawExchange)
			}
		}

		symbol := exchange + rawCode

		results = append(results, model.IndexConstituent{
			IndexName: indexName,
			Name:      rawName,
			Symbol:    symbol,
		})
	}

	return results, nil
}
