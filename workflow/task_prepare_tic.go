package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

// ExtraTicValidDates 是 prepare_tic 写入 args.Extra 的 key，
// 值类型为 []time.Time，列出本轮成功下载到的分时日期。
const ExtraTicValidDates = "tic_valid_dates"

var TaskPrepareTic *Task

func init() {
	TaskPrepareTic = &Task{
		Name:      "prepare_tic",
		DependsOn: []string{},
		SkipIf: func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
			return !args.Min
		},
		Executor: executePrepareTic,
	}
	registerTask(TaskPrepareTic, "update")
}

func executePrepareTic(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	latest, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
	if err != nil {
		return nil, fmt.Errorf("query 1min latest: %w", err)
	}
	if latest.IsZero() {
		fmt.Println("🛑 数据库中没有分时数据，历史请自行导入")
		found, err := findLatestTicDate(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("探测最新分时日期失败: %w", err)
		}
		if found.IsZero() {
			fmt.Println("⚠️  近7天均无可用分时数据，跳过")
			return &TaskResult{State: StateSkipped, Message: "no tic data available in last 7 days"}, nil
		}
		// 让 pullDateRange 从 found 日期开始（它会从 since+1 起算）
		latest = found.AddDate(0, 0, -1)
	} else {
		fmt.Printf("📅 分时数据最新日期为 %s\n", latest.Format("2006-01-02"))
	}

	src := pullSource{
		targetDir:   filepath.Join(args.VipdocDir, "newdatetick"),
		urlTemplate: "https://www.tdx.com.cn/products/data/data/g4tic/%s.zip",
		fileSuffix:  "tic",
		label:       "分时",
	}
	validDates, err := pullDateRange(ctx, latest, src, args)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare tic data: %w", err)
	}

	if len(validDates) >= 30 {
		return nil, fmt.Errorf("分时数据超过30天未更新，请手动补齐后继续")
	}

	if args.Extra == nil {
		args.Extra = map[string]interface{}{}
	}
	args.Extra[ExtraTicValidDates] = validDates

	if len(validDates) == 0 {
		return &TaskResult{State: StateSkipped, Message: "no new tic data"}, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	endDate := validDates[len(validDates)-1]
	fmt.Printf("🐌 开始转档分笔数据\n")
	if err := tdx.DatatoolCreate(args.TempDir, "tick", endDate); err != nil {
		return nil, fmt.Errorf("failed to run DatatoolTickCreate: %w", err)
	}

	return &TaskResult{State: StateCompleted, Rows: len(validDates), Message: "tic prepared"}, nil
}

// findLatestTicDate 从 today 起向前逐日探测，最多7天，
// 返回第一个在通达信服务器上存在（HTTP 200）的日期。
// 探测过程不解压文件，找到即停止。
func findLatestTicDate(ctx context.Context, args *TaskArgs) (time.Time, error) {
	ticURLTemplate := "https://www.tdx.com.cn/products/data/data/g4tic/%s.zip"
	targetDir := filepath.Join(args.VipdocDir, "newdatetick")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return time.Time{}, fmt.Errorf("failed to create target directory: %w", err)
	}

	for i := 0; i < 7; i++ {
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		default:
		}

		d := args.Today.AddDate(0, 0, -i)
		dateStr := d.Format("20060102")
		url := fmt.Sprintf(ticURLTemplate, dateStr)
		filePath := filepath.Join(targetDir, fmt.Sprintf("%stic.zip", dateStr))

		fmt.Printf("🔍 探测分时数据 %s ...\n", dateStr)
		status, err := utils.DownloadFile(url, filePath)
		if err != nil && status != 404 {
			return time.Time{}, fmt.Errorf("download probe failed: %w", err)
		}
		if status == 200 {
			fmt.Printf("✅ 找到最新分时数据: %s\n", dateStr)
			// 解压已下载好的文件
			if err := utils.UnzipFile(filePath, targetDir); err != nil {
				fmt.Printf("⚠️ 解压文件 %s 失败: %v\n", filePath, err)
			}
			return d, nil
		}
	}
	return time.Time{}, nil
}
