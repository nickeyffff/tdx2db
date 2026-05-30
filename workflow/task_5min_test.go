package workflow

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

func TestAlignTo5Min(t *testing.T) {
	tests := []struct {
		input    time.Time
		expected time.Time
	}{
		{
			input:    time.Date(2026, 5, 29, 9, 31, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 9, 35, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 9, 35, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 9, 35, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 9, 36, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 9, 40, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 11, 30, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 11, 30, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 13, 1, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 13, 5, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 14, 59, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 15, 0, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 5, 29, 15, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 29, 15, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		actual := alignTo5Min(tc.input)
		if !actual.Equal(tc.expected) {
			t.Errorf("alignTo5Min(%v) = %v; expected %v", tc.input, actual, tc.expected)
		}
	}
}

func TestAggregateBars(t *testing.T) {
	bars := []model.KlineMin{
		{Symbol: "sh600000", Open: 10.0, High: 10.5, Low: 9.9, Close: 10.2, Volume: 100, Amount: 1000.0, Datetime: time.Date(2026, 5, 29, 9, 31, 0, 0, time.UTC)},
		{Symbol: "sh600000", Open: 10.2, High: 10.3, Low: 10.1, Close: 10.15, Volume: 150, Amount: 1520.0, Datetime: time.Date(2026, 5, 29, 9, 32, 0, 0, time.UTC)},
		{Symbol: "sh600000", Open: 10.15, High: 10.6, Low: 10.0, Close: 10.5, Volume: 200, Amount: 2100.0, Datetime: time.Date(2026, 5, 29, 9, 33, 0, 0, time.UTC)},
	}

	alignedTime := time.Date(2026, 5, 29, 9, 35, 0, 0, time.UTC)
	agg := aggregateBars(bars, "sh600000", alignedTime)

	if agg.Symbol != "sh600000" {
		t.Errorf("expected Symbol sh600000, got %s", agg.Symbol)
	}
	if agg.Open != 10.0 {
		t.Errorf("expected Open 10.0, got %f", agg.Open)
	}
	if agg.High != 10.6 {
		t.Errorf("expected High 10.6, got %f", agg.High)
	}
	if agg.Low != 9.9 {
		t.Errorf("expected Low 9.9, got %f", agg.Low)
	}
	if agg.Close != 10.5 {
		t.Errorf("expected Close 10.5, got %f", agg.Close)
	}
	if agg.Volume != 450 {
		t.Errorf("expected Volume 450, got %d", agg.Volume)
	}
	if agg.Amount != 4620.0 {
		t.Errorf("expected Amount 4620.0, got %f", agg.Amount)
	}
	if !agg.Datetime.Equal(alignedTime) {
		t.Errorf("expected Datetime %v, got %v", alignedTime, agg.Datetime)
	}
}

func TestSynthesize5MinData(t *testing.T) {
	// Ensure directory exists
	_ = os.MkdirAll("/tmp/tdx2db_compare", 0755)

	// Initialize driver
	db, err := database.NewDB("duckdb:///tmp/tdx2db_compare/test.db")
	if err != nil {
		t.Fatalf("failed to create database driver: %v", err)
	}
	if err := db.Connect(); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	// Truncate tables
	if err := db.TruncateTable(model.TableKline1Min); err != nil {
		t.Fatalf("failed to truncate raw_kline_1min: %v", err)
	}
	if err := db.TruncateTable(model.TableKline5Min); err != nil {
		t.Fatalf("failed to truncate raw_stocks_5min: %v", err)
	}

	// Insert mock 1-minute data
	csvPath := "/tmp/tdx2db_compare/mock_1min.csv"
	cw, err := utils.NewCSVWriter[model.KlineMin](csvPath)
	if err != nil {
		t.Fatalf("failed to create CSV writer: %v", err)
	}

	dateStr := "2026-05-29 "
	loc, _ := time.LoadLocation("Asia/Shanghai")
	parseTime := func(s string) time.Time {
		tVal, _ := time.ParseInLocation("2006-01-02 15:04:05", dateStr+s, loc)
		return tVal
	}

	bars := []model.KlineMin{
		{Symbol: "sz000001", Open: 10.0, High: 10.2, Low: 9.9, Close: 10.1, Volume: 100, Amount: 1000.0, Datetime: parseTime("09:31:00")},
		{Symbol: "sz000001", Open: 10.1, High: 10.3, Low: 10.0, Close: 10.2, Volume: 150, Amount: 1500.0, Datetime: parseTime("09:32:00")},
		{Symbol: "sz000001", Open: 10.2, High: 10.4, Low: 10.1, Close: 10.1, Volume: 200, Amount: 2040.0, Datetime: parseTime("09:33:00")},
		{Symbol: "sz000001", Open: 10.1, High: 10.3, Low: 10.0, Close: 10.3, Volume: 120, Amount: 1220.0, Datetime: parseTime("09:34:00")},
		{Symbol: "sz000001", Open: 10.3, High: 10.5, Low: 10.2, Close: 10.4, Volume: 180, Amount: 1850.0, Datetime: parseTime("09:35:00")},
		{Symbol: "sz000001", Open: 10.4, High: 10.6, Low: 10.3, Close: 10.3, Volume: 110, Amount: 1140.0, Datetime: parseTime("09:36:00")},
		{Symbol: "sz000001", Open: 10.3, High: 10.5, Low: 10.2, Close: 10.5, Volume: 130, Amount: 1340.0, Datetime: parseTime("09:37:00")},
		{Symbol: "sz000001", Open: 10.5, High: 10.7, Low: 10.4, Close: 10.6, Volume: 140, Amount: 1480.0, Datetime: parseTime("09:38:00")},
		{Symbol: "sz000001", Open: 10.6, High: 10.8, Low: 10.5, Close: 10.5, Volume: 160, Amount: 1700.0, Datetime: parseTime("09:39:00")},
		{Symbol: "sz000001", Open: 10.5, High: 10.7, Low: 10.4, Close: 10.7, Volume: 190, Amount: 2010.0, Datetime: parseTime("09:40:00")},
	}

	if err := cw.Write(bars); err != nil {
		t.Fatalf("failed to write mock bars: %v", err)
	}
	cw.Close()

	if err := db.ImportKline1Min(csvPath); err != nil {
		t.Fatalf("failed to import mock 1min bars: %v", err)
	}

	// Run executeUpdate5Min
	args := &TaskArgs{
		Min:     true,
		TempDir: "/tmp/tdx2db_compare",
		Today:   time.Date(2026, 5, 30, 0, 0, 0, 0, loc),
	}

	res, err := executeUpdate5Min(context.Background(), db, args)
	if err != nil {
		t.Fatalf("executeUpdate5Min failed: %v", err)
	}

	if res.State != StateCompleted {
		t.Fatalf("expected state Completed, got %s, msg: %s", res.State, res.Message)
	}

	// Verify the result from raw_stocks_5min
	var results []model.KlineMin
	if err := db.Query("raw_stocks_5min", nil, &results); err != nil {
		t.Fatalf("failed to query raw_stocks_5min: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 aggregated bars, got %d", len(results))
	}

	// Verify each bar
	for _, bar := range results {
		if bar.Symbol != "sz000001" {
			t.Errorf("expected symbol sz000001, got %s", bar.Symbol)
		}
		if bar.Datetime.Equal(parseTime("09:35:00")) {
			if bar.Open != 10.0 || bar.High != 10.5 || bar.Low != 9.9 || bar.Close != 10.4 {
				t.Errorf("bar 09:35 OHLC mismatch: Open=%f, High=%f, Low=%f, Close=%f", bar.Open, bar.High, bar.Low, bar.Close)
			}
			if bar.Volume != 750 || bar.Amount != 7610.0 {
				t.Errorf("bar 09:35 Vol/Amt mismatch: Vol=%d, Amt=%f", bar.Volume, bar.Amount)
			}
		} else if bar.Datetime.Equal(parseTime("09:40:00")) {
			if bar.Open != 10.4 || bar.High != 10.8 || bar.Low != 10.2 || bar.Close != 10.7 {
				t.Errorf("bar 09:40 OHLC mismatch: Open=%f, High=%f, Low=%f, Close=%f", bar.Open, bar.High, bar.Low, bar.Close)
			}
			if bar.Volume != 730 || bar.Amount != 7670.0 {
				t.Errorf("bar 09:40 Vol/Amt mismatch: Vol=%d, Amt=%f", bar.Volume, bar.Amount)
			}
		} else {
			t.Errorf("unexpected bar datetime: %v", bar.Datetime)
		}
	}
}
