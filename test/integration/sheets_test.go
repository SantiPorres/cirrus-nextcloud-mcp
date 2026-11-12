//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestSpreadsheetRoundTrip(t *testing.T) {
	h := newHarness(t, map[string]string{
		"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_EDITABLE_SPREADSHEETS": "log*.xlsx", "NEXTCLOUD_PROTECTED_SHEETS": "Guide",
	})

	created := h.call("nc_sheet_create", args("target", "/log.xlsx", "sheet", "Decisions", "headers", []any{"ID", "Topic", "Status"}))
	if created["status"] != "workbook created" || created["verified"] != true {
		t.Fatalf("created = %v", created)
	}
	h.fails("nc_sheet_create", args("target", "/budget.xlsx", "sheet", "S", "headers", []any{"A"}), "cannot be modified")

	appended := h.call("nc_sheet_append_rows", args("target", "/log.xlsx", "rows", []any{
		map[string]any{"ID": "D-001", "Topic": "First", "Status": "To confirm"},
		map[string]any{"Topic": "Second", "Status": "=1+1"},
	}))
	if appended["verified"] != true || fmt.Sprint(appended["ids_assigned"]) != "[D-002]" {
		t.Fatalf("appended = %v", appended)
	}

	updated := h.call("nc_sheet_update_cells", args("target", "/log.xlsx", "key_column", "ID", "key_value", "D-001",
		"changes", map[string]any{"Status": "Decided"}, "expected", map[string]any{"Status": "To confirm"}))
	if updated["verified"] != true {
		t.Fatalf("updated = %v", updated)
	}
	h.fails("nc_sheet_update_cells", args("target", "/log.xlsx", "key_column", "ID", "key_value", "D-001",
		"changes", map[string]any{"Status": "Again"}, "expected", map[string]any{"Status": "To confirm"}), "not the expected")

	added := h.call("nc_sheet_create", args("target", "/log.xlsx", "sheet", "Risks", "headers", []any{"ID", "Risk"}))
	if added["status"] != "sheet added" || added["verified"] != true {
		t.Fatalf("added = %v", added)
	}

	table := h.call("nc_sheet_read", args("target", "/log.xlsx"))
	if table["next_id"] != "D-003" || table["total_rows"].(float64) != 2 || fmt.Sprint(table["sheets"]) != "[Decisions Risks]" {
		t.Errorf("table = %v", table)
	}

	// Check the stored file itself, as Nextcloud holds it.
	workbook, err := excelize.OpenReader(bytes.NewReader([]byte(h.get("/log.xlsx"))))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workbook.Close() }()
	if status, _ := workbook.GetCellValue("Decisions", "C2"); status != "Decided" {
		t.Errorf("C2 = %q", status)
	}
	if formula, _ := workbook.GetCellFormula("Decisions", "C3"); formula != "" {
		t.Errorf("text was stored as a formula: %q", formula)
	}
	if text := h.call("nc_read_file", args("target", "/log.xlsx"))["content"].(string); text == "" {
		t.Error("nc_read_file should render the workbook as text")
	}
}
