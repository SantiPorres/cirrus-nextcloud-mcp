package sheets

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// newWorkbook builds a "Log" table with ids, a fill, a date column, a data
// validation and an Excel table, plus a second descriptive sheet.
func newWorkbook(t *testing.T) *excelize.File {
	t.Helper()
	f := excelize.NewFile()
	t.Cleanup(func() { _ = f.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetSheetName("Sheet1", "Log"))
	must(f.SetSheetRow("Log", "A1", &[]any{"ID", "Topic", "Status", "Due", "Action / Result"}))
	must(f.SetSheetRow("Log", "A2", &[]any{"D-001", "First", "Decided"}))
	must(f.SetSheetRow("Log", "A3", &[]any{"D-002", "Second", "To confirm"}))

	fill, err := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFF2CC"}}})
	must(err)
	must(f.SetCellStyle("Log", "A2", "C3", fill))
	dateStyle, err := f.NewStyle(&excelize.Style{NumFmt: 14})
	must(err)
	must(f.SetCellStyle("Log", "D2", "D3", dateStyle))
	must(f.SetCellValue("Log", "D3", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)))
	must(f.SetCellStyle("Log", "D3", "D3", dateStyle))
	must(f.SetRowHeight("Log", 3, 30))

	validation := excelize.NewDataValidation(true)
	validation.SetSqref("C2:C3")
	must(validation.SetDropList([]string{"Decided", "To confirm", "Discarded"}))
	must(f.AddDataValidation("Log", validation))
	must(f.AddTable("Log", &excelize.Table{Range: "A1:E3", Name: "LogTable", StyleName: "TableStyleMedium2"}))

	_, err = f.NewSheet("Guide")
	must(err)
	must(f.SetSheetRow("Guide", "A1", &[]any{"About"}))
	return f
}

func open(t *testing.T, f *excelize.File, name string) *Sheet {
	t.Helper()
	sheet, err := Open(f, name)
	if err != nil {
		t.Fatal(err)
	}
	return sheet
}

// roundTrip saves and reloads a workbook, as the tools do.
func roundTrip(t *testing.T, f *excelize.File) *excelize.File {
	t.Helper()
	buffer, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	again, err := excelize.OpenReader(bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	return again
}

func wantError(t *testing.T, err error) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected a sheets.Error, got %v", err)
	}
}

func TestOpen(t *testing.T) {
	f := newWorkbook(t)
	if open(t, f, "").Name != "Log" || open(t, f, " guide ").Name != "Guide" {
		t.Error("sheet selection")
	}
	_, err := Open(f, "Nope")
	wantError(t, err)

	_ = f.SetCellStr("Log", "F1", "topic")
	_, err = Open(f, "Log")
	wantError(t, err) // duplicated header
	_, _ = f.NewSheet("Blank")
	_, err = Open(f, "Blank")
	wantError(t, err) // no headers
}

func TestRead(t *testing.T) {
	table := open(t, newWorkbook(t), "").Read(0, 200, "")
	if !reflect.DeepEqual(table.Headers, []string{"ID", "Topic", "Status", "Due", "Action / Result"}) {
		t.Errorf("headers = %v", table.Headers)
	}
	if table.TotalRows != 2 || table.LastRowNumber != 3 || table.NextID != "D-003" || table.NextOffset != nil {
		t.Errorf("table = %+v", table)
	}
	second := table.Rows[1]
	if second.Row != 3 || second.Values["Topic"] != "Second" || second.Values["Due"] != "2026-03-15" || second.Values["Action / Result"] != "" {
		t.Errorf("row = %+v", second)
	}

	page := open(t, newWorkbook(t), "").Read(0, 1, "")
	if page.ReturnedRows != 1 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Errorf("page = %+v", page)
	}
	page = open(t, newWorkbook(t), "").Read(1, 1, "")
	if page.Rows[0].Values["ID"] != "D-002" || page.NextOffset != nil {
		t.Errorf("page = %+v", page)
	}
	if other := open(t, newWorkbook(t), "").Read(0, 10, "Topic"); other.NextID != nil {
		t.Errorf("a column without trailing numbers has no next id: %v", other.NextID)
	}
}

func TestValueTypes(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"n", "f", "b", "s", "formula"})
	_ = f.SetSheetRow("Sheet1", "A2", &[]any{7, 2.5, true, "007"})
	_ = f.SetCellFormula("Sheet1", "E2", "=1+1")
	sheet := open(t, f, "")
	got := []any{sheet.Value(2, 1), sheet.Value(2, 2), sheet.Value(2, 3), sheet.Value(2, 4), sheet.Value(2, 5), sheet.Value(9, 9)}
	want := []any{int64(7), 2.5, true, "007", "", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestNextID(t *testing.T) {
	cases := []struct {
		ids  []any
		want any
	}{
		{[]any{"D-001", "D-002"}, "D-003"},
		{[]any{"D-009", "D-010", "", nil}, "D-011"},
		{[]any{"R-5", "D-2", "D-7", "D-3"}, "D-8"},
		{[]any{int64(3), int64(12), int64(4)}, int64(13)},
		{[]any{"7"}, "8"},
		{[]any{"099"}, "100"},
		{[]any{"alpha"}, nil},
		{nil, nil},
	}
	for _, c := range cases {
		if got := NextID(c.ids); !reflect.DeepEqual(got, c.want) {
			t.Errorf("NextID(%v) = %#v, want %#v", c.ids, got, c.want)
		}
	}
}

func TestAppendAssignsIDsAndCopiesFormat(t *testing.T) {
	f := newWorkbook(t)
	sheet := open(t, f, "Log")
	result, err := sheet.Append([]map[string]any{
		{"topic": "Third", "STATUS": "To confirm", "Due": "2026-04-01"},
		{"Topic": "Fourth", "Action / Result": float64(3)},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAdded != 2 || result.FirstRowNumber != 4 || !reflect.DeepEqual(result.IDsAssigned, []any{"D-003", "D-004"}) {
		t.Errorf("result = %+v", result)
	}

	saved := roundTrip(t, f)
	again := open(t, saved, "Log")
	if again.LastDataRow() != 5 || again.Value(4, 1) != "D-003" || again.Value(5, 2) != "Fourth" || again.Value(5, 5) != int64(3) {
		t.Errorf("saved rows are wrong: %+v", again.Read(0, 10, "").Rows)
	}
	if again.Value(4, 4) != "2026-04-01" {
		t.Errorf("an ISO date in a date column must be stored as a date, got %#v", again.Value(4, 4))
	}
	if kind, _ := saved.GetCellType("Log", "D4"); kind == excelize.CellTypeSharedString || kind == excelize.CellTypeInlineString {
		t.Error("the date was stored as text")
	}

	source, _ := saved.GetCellStyle("Log", "B3")
	for _, cell := range []string{"B4", "B5"} {
		if style, _ := saved.GetCellStyle("Log", cell); style != source || style == 0 {
			t.Errorf("%s did not get the format of the last record", cell)
		}
	}
	if height, _ := saved.GetRowHeight("Log", 5); height != 30 {
		t.Errorf("row height = %v", height)
	}
	validations, _ := saved.GetDataValidations("Log")
	if len(validations) != 1 || validations[0].Sqref != "C2:C5" {
		t.Errorf("validations = %+v", validations)
	}
	tables, _ := saved.GetTables("Log")
	if len(tables) != 1 || tables[0].Range != "A1:E5" || tables[0].Name != "LogTable" {
		t.Errorf("tables = %+v", tables)
	}
}

func TestAppendRefusals(t *testing.T) {
	cases := map[string][]map[string]any{
		"empty":          {},
		"unknown column": {{"Topic": "X"}, {"Missing": "Y"}},
		"duplicate id":   {{"ID": "d-001", "Topic": "X"}},
		"repeated id":    {{"ID": "D-900"}, {"id": "D-900"}},
		"same column":    {{"Topic": "a", "topic": "b"}},
		"bad value":      {{"Topic": []any{"x"}}},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWorkbook(t)
			sheet := open(t, f, "Log")
			_, err := sheet.Append(rows, "")
			wantError(t, err)
			if open(t, f, "Log").LastDataRow() != 3 {
				t.Error("something was written")
			}
		})
	}
}

func TestAppendFormulaIsStoredAsText(t *testing.T) {
	f := newWorkbook(t)
	if _, err := open(t, f, "Log").Append([]map[string]any{{"Topic": "=HYPERLINK(\"http://x\")"}}, ""); err != nil {
		t.Fatal(err)
	}
	saved := roundTrip(t, f)
	if formula, _ := saved.GetCellFormula("Log", "B4"); formula != "" {
		t.Errorf("stored as a formula: %q", formula)
	}
	if value, _ := saved.GetCellValue("Log", "B4"); value != "=HYPERLINK(\"http://x\")" {
		t.Errorf("value = %q", value)
	}
}

func TestAppendWithoutIDColumnOrData(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"PERMISSION", "TYPE"})
	sheet := open(t, f, "")
	result, err := sheet.Append([]map[string]any{{"permission": "p1", "type": "module"}}, "")
	if err != nil || len(result.IDsAssigned) != 0 || result.FirstRowNumber != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	if sheet.Value(2, 1) != "p1" {
		t.Error("the sheet view was not refreshed after appending")
	}

	// A numeric custom id column.
	g := excelize.NewFile()
	defer func() { _ = g.Close() }()
	_ = g.SetSheetRow("Sheet1", "A1", &[]any{"No", "Name"})
	_ = g.SetSheetRow("Sheet1", "A2", &[]any{41, "a"})
	result, err = open(t, g, "").Append([]map[string]any{{"Name": "b"}}, "no")
	if err != nil || !reflect.DeepEqual(result.IDsAssigned, []any{int64(42)}) {
		t.Fatalf("%+v %v", result, err)
	}
	// With ids that cannot be continued, the caller must pass one.
	_ = g.SetCellStr("Sheet1", "A3", "last")
	_, err = open(t, g, "").Append([]map[string]any{{"Name": "c"}}, "No")
	wantError(t, err)
}

func TestUpdate(t *testing.T) {
	f := newWorkbook(t)
	sheet := open(t, f, "Log")
	result, err := sheet.Update("id", "d-002", map[string]any{"status": "Decided", "Action / Result": "Applied", "Topic": ""},
		map[string]any{"Status": "to confirm", "Due": "2026-03-15"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowNumber != 3 || result.Changed["Status"].Before != "To confirm" || result.Changed["Status"].After != "Decided" {
		t.Errorf("result = %+v", result)
	}
	saved := open(t, roundTrip(t, f), "Log")
	if saved.Value(3, 3) != "Decided" || saved.Value(3, 5) != "Applied" || saved.Value(3, 2) != "" || saved.Value(3, 1) != "D-002" {
		t.Errorf("row = %+v", saved.Read(1, 1, "").Rows)
	}
	if style, _ := f.GetCellStyle("Log", "C3"); style == 0 {
		t.Error("the cell lost its format")
	}
}

func TestUpdateRefusals(t *testing.T) {
	type call struct {
		keyColumn string
		keyValue  any
		changes   map[string]any
		expected  map[string]any
	}
	status := map[string]any{"Status": "Decided"}
	cases := map[string]call{
		"no changes":        {"ID", "D-002", nil, nil},
		"missing key":       {"ID", "D-999", status, nil},
		"unknown key col":   {"Nope", "x", status, nil},
		"unknown column":    {"ID", "D-002", map[string]any{"Nope": 1}, nil},
		"unknown expected":  {"ID", "D-002", status, map[string]any{"Nope": 1}},
		"key column":        {"ID", "D-001", map[string]any{"id": "D-777"}, nil},
		"expected mismatch": {"ID", "D-002", status, map[string]any{"Status": "Discarded"}},
		"bad value":         {"ID", "D-002", map[string]any{"Status": map[string]any{}}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWorkbook(t)
			_, err := open(t, f, "Log").Update(c.keyColumn, c.keyValue, c.changes, c.expected)
			wantError(t, err)
			if open(t, f, "Log").Value(3, 3) != "To confirm" {
				t.Error("something was written")
			}
		})
	}

	f := newWorkbook(t)
	_ = f.SetCellStr("Log", "A3", "D-001")
	_, err := open(t, f, "Log").Update("ID", "D-001", status, nil)
	wantError(t, err) // ambiguous key
}

func TestUpdateMatchesNumericKeys(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"ID", "Name"})
	_ = f.SetSheetRow("Sheet1", "A2", &[]any{7, "old"})
	for _, key := range []any{"7", float64(7)} {
		if _, err := open(t, f, "").Update("ID", key, map[string]any{"Name": "new"}, nil); err != nil {
			t.Errorf("key %#v: %v", key, err)
		}
	}
}

func TestCreate(t *testing.T) {
	f, err := NewWorkbook("Decisions", []string{" ID ", "Topic"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if names := SheetNames(f); !reflect.DeepEqual(names, []string{"Decisions"}) {
		t.Errorf("sheets = %v", names)
	}
	if err := AddSheet(f, "Risks", []string{"ID", "Risk"}); err != nil {
		t.Fatal(err)
	}
	saved := roundTrip(t, f)
	if !reflect.DeepEqual(open(t, saved, "Risks").Headers(), []string{"ID", "Risk"}) ||
		!reflect.DeepEqual(open(t, saved, "decisions").Headers(), []string{"ID", "Topic"}) {
		t.Error("headers were not written")
	}

	wantError(t, AddSheet(f, "risks", []string{"A"}))
	wantError(t, AddSheet(f, "Other", nil))
	wantError(t, AddSheet(f, "Other", []string{"A", "a"}))
	wantError(t, AddSheet(f, "Other", []string{"A", " "}))
	wantError(t, AddSheet(f, "bad/name", []string{"A"}))
	_, err = NewWorkbook("bad:name", []string{"A"})
	wantError(t, err)
	if len(SheetNames(f)) != 2 {
		t.Errorf("a refused sheet was added: %v", SheetNames(f))
	}
}
