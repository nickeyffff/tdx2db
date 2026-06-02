package database

import (
	"time"

	"github.com/jing2uo/tdx2db/model"
)

type DataRepository interface {
	Connect() error
	Close() error

	InitSchema() error

	ReadSchemaVersion() (string, error)
	WriteSchemaVersion() error

	ImportCSV(meta *model.TableMeta, csvPath string) error
	ImportKlineDaily(csvPath string) error
	ImportKline1Min(csvPath string) error
	ImportAdjustFactors(csvPath string) error
	ImportGBBQ(csvPath string) error
	ImportBasic(csvPath string) error
	ImportHolidays(csvPath string) error
	ImportBlockInfo(csvPath string) error
	ImportBlockMembers(csvPath string) error
	ImportSymbolNames(csvPath string) error

	TruncateTable(meta *model.TableMeta) error
	ClearDates(tableName string, dateCol string, dates []time.Time) error
	Query(table string, conditions map[string]interface{}, dest interface{}) error
	QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error)
	QueryKline1Min(startDate, endDate time.Time) ([]model.KlineMin, error)
	QueryUniqueDates(tableName string, dateCol string, since *time.Time) ([]time.Time, error)
	GetLatestDate(tableName string, dateCol string) (time.Time, error)
	GetMinDate(tableName string, dateCol string) (time.Time, error)
	GetSymbolsByClass(classes ...string) ([]string, error)
	RebuildSymbolClass() error
	CountKlineDaily() (int64, error)

	GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error)

	GetGbbq() ([]model.GbbqData, error)
	GetHolidays() ([]time.Time, error)
}
