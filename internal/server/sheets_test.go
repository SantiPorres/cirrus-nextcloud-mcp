package server_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/xuri/excelize/v2"
)

var sheetsEnv = map[string]string{
	"NEXTCLOUD_ALLOW_WRITE":           "true",
	"NEXTCLOUD_EDITABLE_SPREADSHEETS": "log*.xlsx, Plans/*/matrix.xlsx",
	"NEXTCLOUD_PROTECTED_SHEETS":      "Guide",
}

func workbookBytes(t *testing.T, build func(f *excelize.File)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	build(f)
	buffer, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func logWorkbook(t *testing.T) []byte {
	return workbookBytes(t, func(f *excelize.File) {
		_ = f.SetSheetName("Sheet1", "Log")
		_ = f.SetSheetRow("Log", "A1", &[]any{"ID", "Topic", "Status"})
		_ = f.SetSheetRow("Log", "A2", &[]any{"D-001", "First", "Decided"})
		_ = f.SetSheetRow("Log", "A3", &[]any{"D-002", "Second", "To confirm"})
		_, _ = f.NewSheet("Guide")
		_ = f.SetSheetRow("Guide", "A1", &[]any{"About"})
	})
}

func (h *harness) workbook(path string) *excelize.File {
	h.t.Helper()
	data, ok := h.fake.File(path)
	if !ok {
		h.t.Fatalf("%s does not exist", path)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = f.Close() })
	return f
}

func (h *harness) cell(path, sheet, cell string) string {
	h.t.Helper()
	value, err := h.workbook(path).GetCellValue(sheet, cell)
	if err != nil {
		h.t.Fatal(err)
	}
	return value
}

func TestSheetToolsAvailability(t *testing.T) {
	names := newHarness(t, nil).toolNames()
	if !slices.Contains(names, "nc_sheet_read") {
		t.Error("nc_sheet_read should be available")
	}
	writeTools := []string{"nc_sheet_append_rows", "nc_sheet_update_cells", "nc_sheet_create"}
	for _, name := range writeTools {
		if slices.Contains(names, name) {
			t.Errorf("%s must not exist on a read-only server", name)
		}
	}
	// Write permission alone is not enough: no spreadsheet pattern is configured.
	names = newHarness(t, writable).toolNames()
	for _, name := range writeTools {
		if slices.Contains(names, name) {
			t.Errorf("%s needs NEXTCLOUD_EDITABLE_SPREADSHEETS", name)
		}
	}
	names = newHarness(t, sheetsEnv).toolNames()
	for _, name := range writeTools {
		if !slices.Contains(names, name) {
			t.Errorf("%s should be available", name)
		}
	}
}

func TestSheetRead(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	id := h.fake.AddFile("/Data/Log 2026.xlsx", logWorkbook(t))
	h.fake.AddFile("/notes.md", []byte("x"))

	for _, target := range []string{"/Data/Log 2026.xlsx", fmt.Sprintf("%s/f/%d", h.fake.URL, id)} {
		table := h.call("nc_sheet_read", args("target", target))
		rows := table["rows"].([]any)
		if table["next_id"] != "D-003" || table["total_rows"].(float64) != 2 || len(rows) != 2 || table["editable"] != true ||
			fmt.Sprint(table["headers"]) != "[ID Topic Status]" || fmt.Sprint(table["sheets"]) != "[Log Guide]" {
			t.Errorf("table = %v", table)
		}
		first := rows[0].(map[string]any)
		if first["row"].(float64) != 2 || first["values"].(map[string]any)["Topic"] != "First" {
			t.Errorf("row = %v", first)
		}
	}
	page := h.call("nc_sheet_read", args("target", "/Data/Log 2026.xlsx", "max_rows", 1))
	if page["next_offset"].(float64) != 1 || page["returned_rows"].(float64) != 1 {
		t.Errorf("page = %v", page)
	}
	if guide := h.call("nc_sheet_read", args("target", "/Data/Log 2026.xlsx", "sheet", "guide")); guide["editable"] != false {
		t.Errorf("a protected sheet is not editable: %v", guide)
	}
	h.fails("nc_sheet_read", args("target", "/Data/Log 2026.xlsx", "sheet", "Nope"), "no sheet named")
	h.fails("nc_sheet_read", args("target", "/notes.md"), "not an .xlsx file")
	h.fake.AddFile("/broken.xlsx", []byte("not a zip"))
	h.fails("nc_sheet_read", args("target", "/broken.xlsx"), "damaged")
}

func TestSheetAppend(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	h.fake.AddFile("/Data/log.xlsx", logWorkbook(t))
	result := h.call("nc_sheet_append_rows", args("target", "/Data/log.xlsx", "rows", []any{
		map[string]any{"topic": "Third", "Status": "=SUM(A1)"},
		map[string]any{"ID": "D-010", "Topic": "Tenth"},
	}))
	if result["verified"] != true || result["first_row_number"].(float64) != 4 || fmt.Sprint(result["ids_assigned"]) != "[D-003]" {
		t.Errorf("result = %v", result)
	}
	if h.cell("/Data/log.xlsx", "Log", "A4") != "D-003" || h.cell("/Data/log.xlsx", "Log", "C4") != "=SUM(A1)" ||
		h.cell("/Data/log.xlsx", "Log", "A5") != "D-010" {
		t.Error("rows were not written as expected")
	}
	if formula, _ := h.workbook("/Data/log.xlsx").GetCellFormula("Log", "C4"); formula != "" {
		t.Errorf("text was stored as a formula: %q", formula)
	}
	if next := h.call("nc_sheet_read", args("target", "/Data/log.xlsx"))["next_id"]; next != "D-011" {
		t.Errorf("next_id = %v", next)
	}

	before, _ := h.fake.File("/Data/log.xlsx")
	h.fails("nc_sheet_append_rows", args("target", "/Data/log.xlsx", "rows", []any{map[string]any{"Nope": 1}}), "Unknown column")
	h.fails("nc_sheet_append_rows", args("target", "/Data/log.xlsx", "rows", []any{map[string]any{"ID": "d-001"}}), "already exists")
	h.fails("nc_sheet_append_rows", args("target", "/Data/log.xlsx", "sheet", "Guide", "rows", []any{map[string]any{"About": "x"}}), "protected")
	if after, _ := h.fake.File("/Data/log.xlsx"); !bytes.Equal(before, after) {
		t.Error("a refused operation changed the file")
	}
	h.assertNoBlindOverwrite()
}

func TestSheetWritesOnlyMatchConfiguredFiles(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	h.fake.AddFile("/Data/budget.xlsx", logWorkbook(t))
	h.fake.AddFile("/Plans/Q1/matrix.xlsx", logWorkbook(t))
	h.fake.AddFile("/Plans/Q1/deep/matrix.xlsx", logWorkbook(t))
	row := []any{map[string]any{"Topic": "x"}}

	h.fails("nc_sheet_append_rows", args("target", "/Data/budget.xlsx", "rows", row), "cannot be modified")
	h.fails("nc_sheet_append_rows", args("target", "/Plans/Q1/deep/matrix.xlsx", "rows", row), "cannot be modified")
	h.fails("nc_sheet_update_cells", args("target", "/Data/budget.xlsx", "key_column", "ID", "key_value", "D-001", "changes", map[string]any{"Topic": "x"}), "cannot be modified")
	h.fails("nc_sheet_create", args("target", "/Data/new.xlsx", "sheet", "S", "headers", []any{"A"}), "cannot be modified")
	h.call("nc_sheet_append_rows", args("target", "/Plans/Q1/matrix.xlsx", "rows", row))
	if h.fake.Exists("/Data/new.xlsx") {
		t.Error("a workbook outside the patterns was created")
	}
}

func TestSheetUpdate(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	h.fake.AddFile("/log.xlsx", logWorkbook(t))
	result := h.call("nc_sheet_update_cells", args("target", "/log.xlsx", "key_column", "id", "key_value", "d-002",
		"changes", map[string]any{"Status": "Decided", "Topic": ""}, "expected", map[string]any{"Status": "To confirm"}))
	changed := result["changed"].(map[string]any)["Status"].(map[string]any)
	if result["verified"] != true || result["row_number"].(float64) != 3 || changed["before"] != "To confirm" || changed["after"] != "Decided" {
		t.Errorf("result = %v", result)
	}
	if h.cell("/log.xlsx", "Log", "C3") != "Decided" || h.cell("/log.xlsx", "Log", "B3") != "" || h.cell("/log.xlsx", "Log", "A3") != "D-002" {
		t.Error("cells were not updated as expected")
	}

	update := func(extra ...any) map[string]any {
		return args(append([]any{"target", "/log.xlsx", "key_column", "ID", "key_value", "D-001"}, extra...)...)
	}
	h.fails("nc_sheet_update_cells", update("changes", map[string]any{"Status": "x"}, "expected", map[string]any{"Status": "Other"}), "not the expected")
	h.fails("nc_sheet_update_cells", update("changes", map[string]any{"ID": "D-9"}), "key column cannot be changed")
	h.fails("nc_sheet_update_cells", update("changes", map[string]any{}), "at least one column")
	h.fails("nc_sheet_update_cells", args("target", "/log.xlsx", "key_column", "ID", "key_value", "D-404", "changes", map[string]any{"Status": "x"}), "No row has")
	if h.cell("/log.xlsx", "Log", "C2") != "Decided" {
		t.Error("a refused update changed the file")
	}
	h.assertNoBlindOverwrite()
}

func TestSheetEditFailsWhenTheFileChangedInBetween(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	h.fake.AddFile("/log.xlsx", logWorkbook(t))
	theirs := workbookBytes(t, func(f *excelize.File) { _ = f.SetSheetRow("Sheet1", "A1", &[]any{"ID", "Topic", "Status", "Theirs"}) })
	h.fake.BeforeNextPut(func() { h.fake.AddFile("/log.xlsx", theirs) })

	h.fails("nc_sheet_append_rows", args("target", "/log.xlsx", "rows", []any{map[string]any{"Topic": "mine"}}), "modified by someone else")
	if after, _ := h.fake.File("/log.xlsx"); !bytes.Equal(after, theirs) {
		t.Error("the other person's version was overwritten")
	}
	h.assertNoBlindOverwrite()
}

func TestSheetCreate(t *testing.T) {
	h := newHarness(t, sheetsEnv)
	h.fake.AddDir("/Data")

	created := h.call("nc_sheet_create", args("target", "/Data/log-new.xlsx", "sheet", "Risks", "headers", []any{"ID", "Risk"}))
	if created["status"] != "workbook created" || created["verified"] != true {
		t.Errorf("created = %v", created)
	}
	if names := h.workbook("/Data/log-new.xlsx").GetSheetList(); fmt.Sprint(names) != "[Risks]" {
		t.Errorf("sheets = %v", names)
	}
	added := h.call("nc_sheet_create", args("target", "/Data/log-new.xlsx", "sheet", "Actions", "headers", []any{"ID", "Action"}))
	if added["status"] != "sheet added" || added["verified"] != true {
		t.Errorf("added = %v", added)
	}
	if names := h.workbook("/Data/log-new.xlsx").GetSheetList(); fmt.Sprint(names) != "[Risks Actions]" || h.cell("/Data/log-new.xlsx", "Actions", "B1") != "Action" {
		t.Errorf("sheets = %v", names)
	}
	// The new table is immediately usable.
	appended := h.call("nc_sheet_append_rows", args("target", "/Data/log-new.xlsx", "sheet", "Actions", "rows", []any{map[string]any{"ID": "A-1", "Action": "Do"}}))
	if appended["verified"] != true || h.cell("/Data/log-new.xlsx", "Actions", "A2") != "A-1" {
		t.Errorf("appended = %v", appended)
	}

	h.fails("nc_sheet_create", args("target", "/Data/log-new.xlsx", "sheet", "risks", "headers", []any{"X"}), "already has a sheet")
	h.fails("nc_sheet_create", args("target", "/Data/log-new.xlsx", "sheet", "Guide", "headers", []any{"X"}), "protected")
	h.fails("nc_sheet_create", args("target", "/Data/log-2.xlsx", "sheet", "S", "headers", []any{}), "at least one column")
	h.fails("nc_sheet_create", args("target", "/Missing/log-3.xlsx", "sheet", "S", "headers", []any{"A"}), "parent folder")
	if h.fake.Exists("/Data/log-2.xlsx") {
		t.Error("a refused workbook was created")
	}
	h.assertNoBlindOverwrite()
}
