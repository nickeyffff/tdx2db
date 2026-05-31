package workflow

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

func TestParseXLSFile(t *testing.T) {
	filePath := "/tmp/000300cons.xls"
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Skip("Skip TestParseXLSFile because /tmp/000300cons.xls does not exist")
	}

	consts, err := parseXLSFile(filePath)
	if err != nil {
		t.Fatalf("parseXLSFile failed: %v", err)
	}

	if len(consts) == 0 {
		t.Fatalf("expected at least some constituents, got 0")
	}

	t.Logf("Parsed %d constituents from %s", len(consts), filePath)

	// Validate a few properties of the first constituent
	first := consts[0]
	if first.IndexName == "" {
		t.Errorf("IndexName should not be empty")
	}
	if first.Name == "" {
		t.Errorf("Name should not be empty")
	}
	if !strings.HasPrefix(first.Symbol, "sh") && !strings.HasPrefix(first.Symbol, "sz") && !strings.HasPrefix(first.Symbol, "bj") {
		t.Errorf("Symbol should have market prefix: %s", first.Symbol)
	}
}

func TestExecuteUpdateIndexConstituent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "tdx2db-test-index-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepathJoin(tempDir, "test.db")
	db, err := database.NewDB("duckdb://" + dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	if err := db.Connect(); err != nil {
		t.Fatalf("failed to connect database: %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	args := &TaskArgs{
		TempDir: tempDir,
	}

	ctx := context.Background()
	result, err := executeUpdateIndexConstituent(ctx, db, args)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "status 403") || strings.Contains(errStr, "403") {
			t.Skipf("Skipping integration test: network request blocked by WAF (403): %v", err)
			return
		}
		t.Fatalf("executeUpdateIndexConstituent failed: %v", err)
	}

	if result.State != StateCompleted {
		t.Errorf("expected state Completed, got %v (msg: %s)", result.State, result.Message)
	}

	// Verify records are inserted into the database
	var count int64
	// Use standard Query to query the table
	var dbRecords []model.IndexConstituent
	err = db.Query("raw_index_constituent", nil, &dbRecords)
	if err != nil {
		t.Fatalf("failed to query raw_index_constituent: %v", err)
	}

	count = int64(len(dbRecords))
	t.Logf("Successfully imported %d index constituent records into database", count)
	if count == 0 {
		t.Errorf("expected some records in database, got 0")
	}

	// Check if we can find some expected indices, like 'CSI300'
	foundCSI300 := false
	for _, rec := range dbRecords {
		if rec.IndexName == "CSI300" {
			foundCSI300 = true
			break
		}
	}
	if !foundCSI300 {
		t.Errorf("expected to find CSI300 constituents in database")
	}
}

func filepathJoin(elem ...string) string {
	return strings.Join(elem, "/")
}

