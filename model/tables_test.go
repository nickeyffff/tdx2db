package model

import (
	"testing"
	"time"
)

func TestSchemaFromStructNullableTag(t *testing.T) {
	type row struct {
		Date time.Time `col:"date" type:"date" nullable:"true"`
	}

	meta := SchemaFromStruct("test_nullable_schema", row{}, []string{"date"})
	if len(meta.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(meta.Columns))
	}
	if !meta.Columns[0].Nullable {
		t.Fatalf("expected nullable column")
	}
}

func TestKlineDailyIncludesBreadthColumns(t *testing.T) {
	want := map[string]bool{"up_count": false, "down_count": false}
	for _, col := range TableKlineDaily.Columns {
		if _, ok := want[col.Name]; ok {
			want[col.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("raw_kline_daily missing %s column", name)
		}
	}
}
