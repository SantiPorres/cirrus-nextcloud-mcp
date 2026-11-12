// Package sheets implements table operations on .xlsx worksheets: the first
// row is the header and every following row is a record. It works on
// in-memory workbooks and has no network access.
package sheets

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// DefaultIDColumn is the header treated as the record identifier when the caller does not name one.
const DefaultIDColumn = "ID"

// Error is a user-facing problem with a table operation. Nothing is modified when it is returned.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func errorf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Sheet is one worksheet seen as a table.
type Sheet struct {
	file *excelize.File
	// Name is the exact sheet name in the workbook.
	Name string

	raw      [][]string // unformatted cell values, trailing blanks trimmed
	titles   map[int]string
	columns  []int          // header columns, in order
	byName   map[string]int // normalised header -> column
	date1904 bool
	dateFmt  map[int]bool // style id -> is a date format
}

// SheetNames lists the sheets of a workbook.
func SheetNames(file *excelize.File) []string { return file.GetSheetList() }

// Open selects a sheet by name (case-insensitive; "" means the first one)
// and reads its header row.
func Open(file *excelize.File, name string) (*Sheet, error) {
	names := file.GetSheetList()
	if len(names) == 0 {
		return nil, errorf("The workbook has no sheets.")
	}
	chosen := names[0]
	if strings.TrimSpace(name) != "" {
		chosen = ""
		for _, candidate := range names {
			if norm(candidate) == norm(name) {
				chosen = candidate
				break
			}
		}
		if chosen == "" {
			return nil, errorf("There is no sheet named %q. Sheets: %s.", name, strings.Join(names, ", "))
		}
	}
	s := &Sheet{file: file, Name: chosen, dateFmt: map[int]bool{}}
	if props, err := file.GetWorkbookProps(); err == nil && props.Date1904 != nil {
		s.date1904 = *props.Date1904
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Sheet) reload() error {
	raw, err := s.file.GetRows(s.Name, excelize.Options{RawCellValue: true})
	if err != nil {
		return errorf("The sheet %q could not be read as a table.", s.Name)
	}
	s.raw = raw
	s.titles, s.byName, s.columns = map[int]string{}, map[string]int{}, nil
	if len(raw) == 0 {
		return errorf("Row 1 of the sheet %q has no headers.", s.Name)
	}
	for index, value := range raw[0] {
		title := strings.TrimSpace(value)
		if title == "" {
			continue
		}
		key := norm(title)
		if _, duplicate := s.byName[key]; duplicate {
			return errorf("The header %q appears more than once in row 1.", title)
		}
		column := index + 1
		s.titles[column], s.byName[key] = title, column
		s.columns = append(s.columns, column)
	}
	if len(s.columns) == 0 {
		return errorf("Row 1 of the sheet %q has no headers.", s.Name)
	}
	return nil
}

// Headers returns the header titles in column order.
func (s *Sheet) Headers() []string {
	out := make([]string, 0, len(s.columns))
	for _, column := range s.columns {
		out = append(out, s.titles[column])
	}
	return out
}

func (s *Sheet) rawAt(row, column int) string {
	if row < 1 || row > len(s.raw) || column < 1 || column > len(s.raw[row-1]) {
		return ""
	}
	return s.raw[row-1][column-1]
}

func cellName(column, row int) string {
	name, _ := excelize.CoordinatesToCellName(column, row)
	return name
}

// LastDataRow is the last row (>= 1) with a value in any header column.
// Rows that are only formatted do not count.
func (s *Sheet) LastDataRow() int {
	for row := len(s.raw); row > 1; row-- {
		for _, column := range s.columns {
			if s.rawAt(row, column) != "" {
				return row
			}
		}
	}
	return 1
}

// --- values -----------------------------------------------------------------

var dateFormatChars = regexp.MustCompile(`(?i)[dmyhs]`)

// stripFormatLiterals removes quoted text, bracketed sections and escaped characters from a number format.
var stripFormatLiterals = regexp.MustCompile(`"[^"]*"|\[[^\]]*\]|\\.|_.|\*.`)

func isDateFormat(style *excelize.Style) bool {
	if style == nil {
		return false
	}
	if style.CustomNumFmt != nil {
		code := stripFormatLiterals.ReplaceAllString(*style.CustomNumFmt, "")
		if strings.EqualFold(strings.TrimSpace(code), "general") {
			return false
		}
		return dateFormatChars.MatchString(code)
	}
	id := style.NumFmt
	return (id >= 14 && id <= 22) || (id >= 27 && id <= 36) || (id >= 45 && id <= 47) || (id >= 50 && id <= 58)
}

func (s *Sheet) isDateCell(row, column int) bool {
	styleID, err := s.file.GetCellStyle(s.Name, cellName(column, row))
	if err != nil {
		return false
	}
	if known, ok := s.dateFmt[styleID]; ok {
		return known
	}
	style, err := s.file.GetStyle(styleID)
	result := err == nil && isDateFormat(style)
	s.dateFmt[styleID] = result
	return result
}

func formatDate(serial float64, date1904 bool) (string, bool) {
	moment, err := excelize.ExcelDateToTime(serial, date1904)
	if err != nil {
		return "", false
	}
	switch {
	case serial < 1:
		return moment.Format("15:04:05"), true
	case serial == math.Trunc(serial):
		return moment.Format("2006-01-02"), true
	}
	return moment.Format("2006-01-02T15:04:05"), true
}

// Value returns a cell as a JSON-friendly value: text, a number, a boolean,
// an ISO date for date-formatted cells, or "" when empty. Formulas yield
// their last calculated value.
func (s *Sheet) Value(row, column int) any {
	raw := s.rawAt(row, column)
	if raw == "" {
		return ""
	}
	kind, err := s.file.GetCellType(s.Name, cellName(column, row))
	if err != nil {
		return raw
	}
	switch kind {
	case excelize.CellTypeBool:
		return raw == "1" || strings.EqualFold(raw, "true")
	case excelize.CellTypeUnset, excelize.CellTypeNumber:
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return raw
		}
		if s.isDateCell(row, column) {
			if text, ok := formatDate(number, s.date1904); ok {
				return text
			}
		}
		if number == math.Trunc(number) && math.Abs(number) < 1e15 {
			return int64(number)
		}
		return number
	}
	return raw
}

// plain renders any cell or JSON value as the text used for comparisons.
func plain(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

// Same compares two values the way a person would: ignoring case, surrounding
// spaces and the difference between 7 and "7".
func Same(a, b any) bool { return norm(plain(a)) == norm(plain(b)) }

func isBlank(value any) bool {
	text, isText := value.(string)
	return value == nil || (isText && text == "")
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?)?$`)

func parseISODate(text string) (time.Time, bool) {
	if !isoDate.MatchString(text) {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if moment, err := time.Parse(layout, text); err == nil {
			return moment, true
		}
	}
	return time.Time{}, false
}

// set writes a value keeping the cell's style. Text is always stored as
// text, never as a formula, even when it starts with "=". An ISO date
// written into a date-formatted cell is stored as a real date.
func (s *Sheet) set(row, column int, value any) error {
	cell := cellName(column, row)
	var err error
	switch v := value.(type) {
	case nil:
		err = s.file.SetCellValue(s.Name, cell, nil)
	case string:
		switch moment, isDate := parseISODate(v); {
		case v == "":
			err = s.file.SetCellValue(s.Name, cell, nil)
		case isDate && s.isDateCell(row, column):
			styleID, _ := s.file.GetCellStyle(s.Name, cell)
			if err = s.file.SetCellValue(s.Name, cell, moment); err == nil {
				err = s.file.SetCellStyle(s.Name, cell, cell, styleID)
			}
		default:
			err = s.file.SetCellStr(s.Name, cell, v)
		}
	case bool:
		err = s.file.SetCellBool(s.Name, cell, v)
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			err = s.file.SetCellInt(s.Name, cell, int64(v))
		} else {
			err = s.file.SetCellFloat(s.Name, cell, v, -1, 64)
		}
	case int:
		err = s.file.SetCellInt(s.Name, cell, int64(v))
	case int64:
		err = s.file.SetCellInt(s.Name, cell, v)
	default:
		return errorf("Cell values must be text, numbers or booleans (column %q). Nothing was saved.", s.titles[column])
	}
	if err != nil {
		return errorf("Could not write cell %s. Nothing was saved.", cell)
	}
	return nil
}

func checkValue(value any) bool {
	switch value.(type) {
	case nil, string, bool, float64, int, int64:
		return true
	}
	return false
}

// resolve maps header names to columns and fails listing the unknown ones.
func (s *Sheet) resolve(names []string, what string) (map[string]int, error) {
	out := map[string]int{}
	var unknown []string
	for _, name := range names {
		column, ok := s.byName[norm(name)]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		out[name] = column
	}
	if len(unknown) > 0 {
		return nil, errorf("Unknown column(s) in %s: %s. Columns in the sheet: %s. Nothing was saved.",
			what, strings.Join(unknown, ", "), strings.Join(s.Headers(), ", "))
	}
	return out, nil
}

// --- ids --------------------------------------------------------------------

var trailingNumber = regexp.MustCompile(`^(.*?)(\d+)$`)

type idParts struct {
	prefix string
	number int64
	width  int
}

func splitID(text string) (idParts, bool) {
	m := trailingNumber.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return idParts{}, false
	}
	number, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return idParts{}, false
	}
	width := 0
	if strings.HasPrefix(m[2], "0") {
		width = len(m[2])
	}
	return idParts{prefix: m[1], number: number, width: width}, true
}

// NextID returns the next free id after the highest one sharing the prefix
// of the last id: "D-007" -> "D-008", 12 -> 13. It returns nil when the ids
// have no trailing number. Numeric ids stay numbers.
func NextID(ids []any) any {
	var values []any
	for _, id := range ids {
		if !isBlank(id) {
			values = append(values, id)
		}
	}
	if len(values) == 0 {
		return nil
	}
	lastValue := values[len(values)-1]
	last, ok := splitID(plain(lastValue))
	if !ok {
		return nil
	}
	highest := last.number
	for _, value := range values {
		if parts, ok := splitID(plain(value)); ok && parts.prefix == last.prefix && parts.number > highest {
			highest = parts.number
		}
	}
	if _, isText := lastValue.(string); !isText && last.prefix == "" {
		return highest + 1
	}
	return last.prefix + fmt.Sprintf("%0*d", last.width, highest+1)
}

func (s *Sheet) idValues(column, lastRow int) []any {
	ids := make([]any, 0, max(0, lastRow-1))
	for row := 2; row <= lastRow; row++ {
		ids = append(ids, s.Value(row, column))
	}
	return ids
}

// --- reading ----------------------------------------------------------------

// Record is one data row.
type Record struct {
	Row    int            `json:"row"`
	Values map[string]any `json:"values"`
}

// Table is a page of a sheet read as a table.
type Table struct {
	Sheet         string   `json:"sheet"`
	Headers       []string `json:"headers"`
	TotalRows     int      `json:"total_rows"`
	ReturnedRows  int      `json:"returned_rows"`
	NextOffset    *int     `json:"next_offset,omitempty"`
	NextID        any      `json:"next_id,omitempty"`
	LastRowNumber int      `json:"last_row_number"`
	Rows          []Record `json:"rows"`
}

// Read returns up to maxRows records starting after offset records.
// idColumn names the identifier column used to suggest the next free id.
func (s *Sheet) Read(offset, maxRows int, idColumn string) Table {
	offset, maxRows = max(0, offset), max(1, maxRows)
	last := s.LastDataRow()
	first := 2 + offset
	end := min(last, first+maxRows-1)

	rows := []Record{}
	for row := first; row <= end; row++ {
		values := map[string]any{}
		empty := true
		for _, column := range s.columns {
			value := s.Value(row, column)
			values[s.titles[column]] = value
			empty = empty && value == ""
		}
		if !empty {
			rows = append(rows, Record{Row: row, Values: values})
		}
	}
	table := Table{
		Sheet: s.Name, Headers: s.Headers(), TotalRows: max(0, last-1), ReturnedRows: len(rows),
		LastRowNumber: last, Rows: rows,
	}
	if end < last {
		next := offset + maxRows
		table.NextOffset = &next
	}
	if column, ok := s.byName[norm(idColumnOrDefault(idColumn))]; ok {
		table.NextID = NextID(s.idValues(column, last))
	}
	return table
}

func idColumnOrDefault(name string) string {
	if strings.TrimSpace(name) == "" {
		return DefaultIDColumn
	}
	return name
}

// --- appending --------------------------------------------------------------

// Appended describes the rows added by Append.
type Appended struct {
	RowsAdded      int   `json:"rows_added"`
	FirstRowNumber int   `json:"first_row_number"`
	IDsAssigned    []any `json:"ids_assigned"`
}

// Append adds records after the last one, copying the format of the last
// record. Row keys must be headers of the sheet (case-insensitive); if any
// is not, nothing is written. When the sheet has the id column and a row has
// no id, the next free one is assigned; a duplicate id is refused.
func (s *Sheet) Append(rows []map[string]any, idColumn string) (Appended, error) {
	if len(rows) == 0 {
		return Appended{}, errorf("rows must contain at least one row.")
	}
	var names []string
	seen := map[string]bool{}
	for index, row := range rows {
		for key, value := range row {
			if !checkValue(value) {
				return Appended{}, errorf("Cell values must be text, numbers or booleans (row %d, column %q). Nothing was saved.", index+1, key)
			}
			if !seen[key] {
				seen[key] = true
				names = append(names, key)
			}
		}
	}
	wanted, err := s.resolve(names, "rows")
	if err != nil {
		return Appended{}, err
	}

	oldLast := s.LastDataRow()
	idCol, hasID := s.byName[norm(idColumnOrDefault(idColumn))]
	var running []any
	taken := map[string]bool{}
	if hasID {
		running = s.idValues(idCol, oldLast)
		for _, id := range running {
			if !isBlank(id) {
				taken[norm(plain(id))] = true
			}
		}
	}

	prepared := make([]map[int]any, 0, len(rows))
	assigned := []any{}
	for index, row := range rows {
		values := map[int]any{}
		for key, value := range row {
			column := wanted[key]
			if _, twice := values[column]; twice {
				return Appended{}, errorf("Row %d sets the column %q more than once. Nothing was saved.", index+1, s.titles[column])
			}
			values[column] = value
		}
		if hasID {
			given := values[idCol]
			switch {
			case isBlank(given):
				id := NextID(running)
				if id == nil {
					return Appended{}, errorf("Row %d has no %s and the next one cannot be worked out from the existing values. Pass it explicitly. Nothing was saved.", index+1, s.titles[idCol])
				}
				values[idCol] = id
				assigned = append(assigned, id)
			case taken[norm(plain(given))]:
				return Appended{}, errorf("The %s %q already exists in the sheet (row %d of the request). Nothing was saved.", s.titles[idCol], plain(given), index+1)
			}
			taken[norm(plain(values[idCol]))] = true
			running = append(running, values[idCol])
		}
		prepared = append(prepared, values)
	}

	firstNew := oldLast + 1
	newLast := oldLast + len(prepared)
	lastColumn := s.lastColumn()
	hasSource := oldLast >= 2
	var sourceHeight float64
	if hasSource {
		sourceHeight, _ = s.file.GetRowHeight(s.Name, oldLast)
	}
	for offset, values := range prepared {
		target := firstNew + offset
		if hasSource {
			for column := 1; column <= lastColumn; column++ {
				styleID, err := s.file.GetCellStyle(s.Name, cellName(column, oldLast))
				if err != nil || styleID == 0 {
					continue
				}
				cell := cellName(column, target)
				if err := s.file.SetCellStyle(s.Name, cell, cell, styleID); err != nil {
					return Appended{}, errorf("Could not copy the format to row %d. Nothing was saved.", target)
				}
			}
			if sourceHeight > 0 && sourceHeight != defaultRowHeight {
				_ = s.file.SetRowHeight(s.Name, target, sourceHeight)
			}
		}
		for column, value := range values {
			if isBlank(value) {
				continue
			}
			if err := s.set(target, column, value); err != nil {
				return Appended{}, err
			}
		}
	}
	if hasSource {
		if err := s.growStructures(oldLast, newLast); err != nil {
			return Appended{}, err
		}
	}
	if err := s.reload(); err != nil {
		return Appended{}, err
	}
	return Appended{RowsAdded: len(prepared), FirstRowNumber: firstNew, IDsAssigned: assigned}, nil
}

const defaultRowHeight = 15

func (s *Sheet) lastColumn() int {
	last := s.columns[len(s.columns)-1]
	if dimension, err := s.file.GetSheetDimension(s.Name); err == nil {
		if _, _, maxColumn, _, ok := parseRange(dimension); ok {
			last = max(last, maxColumn)
		}
	}
	return min(last, 16384)
}

// parseRange reads "A1:D9" or "A1" (with optional "$") into coordinates.
func parseRange(ref string) (minColumn, minRow, maxColumn, maxRow int, ok bool) {
	ref = strings.ReplaceAll(ref, "$", "")
	first, second, isRange := strings.Cut(ref, ":")
	minColumn, minRow, err := excelize.CellNameToCoordinates(first)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	if !isRange {
		return minColumn, minRow, minColumn, minRow, true
	}
	maxColumn, maxRow, err = excelize.CellNameToCoordinates(second)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return minColumn, minRow, maxColumn, maxRow, true
}

func formatRange(minColumn, minRow, maxColumn, maxRow int) string {
	return cellName(minColumn, minRow) + ":" + cellName(maxColumn, maxRow)
}

// growStructures keeps data validations, Excel tables and the sheet's
// autofilter covering the appended rows.
func (s *Sheet) growStructures(oldLast, newLast int) error {
	failed := errorf("Could not extend the validations or tables of the sheet to the new rows. Nothing was saved.")

	validations, err := s.file.GetDataValidations(s.Name)
	if err != nil {
		return failed
	}
	for _, validation := range validations {
		ranges := strings.Fields(validation.Sqref)
		changed := false
		for i, ref := range ranges {
			minColumn, minRow, maxColumn, maxRow, ok := parseRange(ref)
			if ok && minRow <= oldLast && maxRow >= oldLast && maxRow < newLast {
				ranges[i] = formatRange(minColumn, minRow, maxColumn, newLast)
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := s.file.DeleteDataValidation(s.Name, validation.Sqref); err != nil {
			return failed
		}
		validation.Sqref = strings.Join(ranges, " ")
		if err := s.file.AddDataValidation(s.Name, validation); err != nil {
			return failed
		}
	}

	tables, err := s.file.GetTables(s.Name)
	if err != nil {
		return failed
	}
	for _, table := range tables {
		minColumn, minRow, maxColumn, maxRow, ok := parseRange(table.Range)
		if !ok || maxRow != oldLast {
			continue
		}
		if err := s.file.DeleteTable(table.Name); err != nil {
			return failed
		}
		table.Range = formatRange(minColumn, minRow, maxColumn, newLast)
		if err := s.file.AddTable(s.Name, &table); err != nil {
			return failed
		}
	}

	for _, name := range s.file.GetDefinedName() {
		if name.Name != "_xlnm._FilterDatabase" || name.Scope != s.Name {
			continue
		}
		ref := name.RefersTo[strings.LastIndex(name.RefersTo, "!")+1:]
		minColumn, minRow, maxColumn, maxRow, ok := parseRange(ref)
		if !ok || maxRow != oldLast {
			continue
		}
		if err := s.file.AutoFilter(s.Name, formatRange(minColumn, minRow, maxColumn, newLast), nil); err != nil {
			return failed
		}
	}
	return nil
}

// --- updating ---------------------------------------------------------------

// Change is the before and after of one cell.
type Change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// Updated describes what Update changed.
type Updated struct {
	RowNumber int               `json:"row_number"`
	Changed   map[string]Change `json:"changed"`
}

// Update changes cells of the single record whose keyColumn equals keyValue.
// expected lists current values that must still hold before anything is
// written, so that someone else's edit is not overwritten.
func (s *Sheet) Update(keyColumn string, keyValue any, changes, expected map[string]any) (Updated, error) {
	if len(changes) == 0 {
		return Updated{}, errorf("changes must contain at least one column.")
	}
	keyColumns, err := s.resolve([]string{keyColumn}, "key_column")
	if err != nil {
		return Updated{}, err
	}
	key := keyColumns[keyColumn]
	changeColumns, err := s.resolve(keysOf(changes), "changes")
	if err != nil {
		return Updated{}, err
	}
	expectedColumns, err := s.resolve(keysOf(expected), "expected")
	if err != nil {
		return Updated{}, err
	}
	usedColumns := map[int]bool{}
	for name, column := range changeColumns {
		if column == key {
			return Updated{}, errorf("The key column cannot be changed.")
		}
		if usedColumns[column] {
			return Updated{}, errorf("changes sets the column %q more than once. Nothing was saved.", s.titles[column])
		}
		usedColumns[column] = true
		if !checkValue(changes[name]) {
			return Updated{}, errorf("Cell values must be text, numbers or booleans (column %q). Nothing was saved.", name)
		}
	}

	last := s.LastDataRow()
	var matches []string
	row := 0
	for candidate := 2; candidate <= last; candidate++ {
		if Same(s.Value(candidate, key), keyValue) {
			matches = append(matches, strconv.Itoa(candidate))
			row = candidate
		}
	}
	switch len(matches) {
	case 0:
		return Updated{}, errorf("No row has %q in the column %q. Nothing was saved.", plain(keyValue), s.titles[key])
	case 1:
	default:
		return Updated{}, errorf("%d rows (rows %s) have %q in %q. Nothing was saved.",
			len(matches), strings.Join(matches, ", "), plain(keyValue), s.titles[key])
	}

	for name, column := range expectedColumns {
		if current := s.Value(row, column); !Same(current, expected[name]) {
			return Updated{}, errorf("%q in row %d is now %q, not the expected %q. Someone may have edited it; read the sheet again. Nothing was saved.",
				s.titles[column], row, plain(current), plain(expected[name]))
		}
	}

	result := Updated{RowNumber: row, Changed: map[string]Change{}}
	for name, column := range changeColumns {
		result.Changed[s.titles[column]] = Change{Before: s.Value(row, column), After: changes[name]}
		if err := s.set(row, column, changes[name]); err != nil {
			return Updated{}, err
		}
	}
	if err := s.reload(); err != nil {
		return Updated{}, err
	}
	return result, nil
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

// --- creating ---------------------------------------------------------------

func cleanHeaders(headers []string) ([]string, error) {
	if len(headers) == 0 {
		return nil, errorf("headers must contain at least one column name.")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(headers))
	for _, header := range headers {
		header = strings.TrimSpace(header)
		if header == "" {
			return nil, errorf("headers must not contain empty names.")
		}
		if seen[norm(header)] {
			return nil, errorf("The header %q is repeated.", header)
		}
		seen[norm(header)] = true
		out = append(out, header)
	}
	return out, nil
}

func writeHeaders(file *excelize.File, sheet string, headers []string) error {
	bold, err := file.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return errorf("Could not prepare the header row.")
	}
	for index, header := range headers {
		cell := cellName(index+1, 1)
		if err := file.SetCellStr(sheet, cell, header); err != nil {
			return errorf("Could not write the header row.")
		}
		if err := file.SetCellStyle(sheet, cell, cell, bold); err != nil {
			return errorf("Could not write the header row.")
		}
	}
	return nil
}

// NewWorkbook creates a workbook with one sheet that has the given header row.
func NewWorkbook(sheet string, headers []string) (*excelize.File, error) {
	headers, err := cleanHeaders(headers)
	if err != nil {
		return nil, err
	}
	file := excelize.NewFile()
	initial := file.GetSheetName(0)
	if err := file.SetSheetName(initial, sheet); err != nil {
		_ = file.Close()
		return nil, errorf("%q is not a valid sheet name (at most 31 characters, none of : \\ / ? * [ ]).", sheet)
	}
	if err := writeHeaders(file, sheet, headers); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// AddSheet adds a sheet with the given header row to an existing workbook.
// It refuses to touch a sheet that already exists.
func AddSheet(file *excelize.File, sheet string, headers []string) error {
	headers, err := cleanHeaders(headers)
	if err != nil {
		return err
	}
	for _, existing := range file.GetSheetList() {
		if norm(existing) == norm(sheet) {
			return errorf("The workbook already has a sheet named %q. Nothing was saved.", existing)
		}
	}
	if _, err := file.NewSheet(sheet); err != nil {
		return errorf("%q is not a valid sheet name (at most 31 characters, none of : \\ / ? * [ ]).", sheet)
	}
	return writeHeaders(file, sheet, headers)
}
