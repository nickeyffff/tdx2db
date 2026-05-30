package workflow

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

var TaskUpdate5Min *Task

func init() {
	TaskUpdate5Min = &Task{
		Name:      "update_5min",
		DependsOn: []string{"update_1min"},
		SkipIf: func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
			return !args.Min
		},
		Executor: executeUpdate5Min,
		OnError:  ErrorModeSkip,
	}
	registerTask(TaskUpdate5Min, "update")
}

func toDateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func alignTo5Min(t time.Time) time.Time {
	m := t.Minute()
	h := t.Hour()
	newMin := ((m + 4) / 5) * 5
	newHour := h
	if newMin >= 60 {
		newMin = 0
		newHour++
	}
	return time.Date(t.Year(), t.Month(), t.Day(), newHour, newMin, 0, 0, t.Location())
}

func aggregateBars(bars []model.KlineMin, symbol string, alignedTime time.Time) model.KlineMin {
	open := bars[0].Open
	close := bars[len(bars)-1].Close
	high := bars[0].High
	low := bars[0].Low
	var volume int64
	var amount float64
	for _, b := range bars {
		if b.High > high {
			high = b.High
		}
		if b.Low < low {
			low = b.Low
		}
		volume += b.Volume
		amount += b.Amount
	}
	return model.KlineMin{
		Symbol:   symbol,
		Open:     open,
		High:     high,
		Low:      low,
		Close:    close,
		Amount:   math.Round(amount*100) / 100,
		Volume:   volume,
		Datetime: alignedTime,
	}
}

func executeUpdate5Min(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	// 1. 获取两表的最新日期
	latest5Min, err := db.GetLatestDate(model.TableKline5Min.TableName, "datetime")
	if err != nil {
		return nil, fmt.Errorf("failed to query raw_stocks_5min latest: %w", err)
	}

	latest1Min, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
	if err != nil {
		return nil, fmt.Errorf("failed to query raw_kline_1min latest: %w", err)
	}

	if latest1Min.IsZero() {
		fmt.Println("🌲 数据库中没有分时(1分钟)数据，跳过5分钟数据合成")
		return &TaskResult{State: StateSkipped, Message: "no 1min data available for synthesis"}, nil
	}

	// 2. 检测 5 分钟数据是否已完整 (按照日线探测模式)
	if !latest5Min.IsZero() && !toDateOnly(latest5Min).Before(toDateOnly(latest1Min)) {
		fmt.Println("🌲 5分钟数据已是最新，无需合成")
		return &TaskResult{State: StateCompleted, Message: "5min data is up to date"}, nil
	}

	// 3. 计算缺失的日期
	var missingDates []time.Time
	if latest5Min.IsZero() {
		missingDates, err = db.QueryUniqueDates(model.TableKline1Min.TableName, "datetime", nil)
	} else {
		missingDates, err = db.QueryUniqueDates(model.TableKline1Min.TableName, "datetime", &latest5Min)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to query missing dates from 1min table: %w", err)
	}

	if len(missingDates) == 0 {
		fmt.Println("🌲 5分钟数据没有缺少的新日期")
		return &TaskResult{State: StateCompleted, Message: "no missing dates found"}, nil
	}

	fmt.Printf("📅 5分钟表不完整，需要从1分钟表合成 %d 天的数据\n", len(missingDates))

	csvPath := filepath.Join(args.TempDir, "stocks_5min.csv")
	cw, err := utils.NewCSVWriter[model.KlineMin](csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSV writer for 5min: %w", err)
	}
	defer cw.Close()

	totalRows := 0
	for _, d := range missingDates {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		startOfDay := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
		endOfDay := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, d.Location())

		k1min, err := db.QueryKline1Min(startOfDay, endOfDay)
		if err != nil {
			return nil, fmt.Errorf("failed to query 1min bars for %s: %w", d.Format("2006-01-02"), err)
		}

		if len(k1min) == 0 {
			continue
		}

		fmt.Printf("🐌 合成 %s 的5分钟K线数据，共 %d 条1-minute数据\n", d.Format("2006-01-02"), len(k1min))

		var currentSymbol string
		var currentAlignedTime time.Time
		var currentGroup []model.KlineMin
		var results []model.KlineMin

		for _, bar := range k1min {
			aligned := alignTo5Min(bar.Datetime)
			if bar.Symbol != currentSymbol || !aligned.Equal(currentAlignedTime) {
				if len(currentGroup) > 0 {
					results = append(results, aggregateBars(currentGroup, currentSymbol, currentAlignedTime))
				}
				currentSymbol = bar.Symbol
				currentAlignedTime = aligned
				currentGroup = []model.KlineMin{bar}
			} else {
				currentGroup = append(currentGroup, bar)
			}
		}

		if len(currentGroup) > 0 {
			results = append(results, aggregateBars(currentGroup, currentSymbol, currentAlignedTime))
		}

		if err := cw.Write(results); err != nil {
			return nil, fmt.Errorf("failed to write 5min bars to CSV for %s: %w", d.Format("2006-01-02"), err)
		}
		totalRows += len(results)
	}

	cw.Close()

	if totalRows > 0 {
		fmt.Printf("📊 开始导入合成的5分钟K线数据至 raw_stocks_5min 表 (%d 条记录)\n", totalRows)
		if err := db.ImportCSV(model.TableKline5Min, csvPath); err != nil {
			return nil, fmt.Errorf("failed to import 5min csv into raw_stocks_5min: %w", err)
		}
		fmt.Println("📊 5分钟K线数据合成与导入成功")
	}

	return &TaskResult{State: StateCompleted, Rows: totalRows, Message: "5min data synthesized"}, nil
}
