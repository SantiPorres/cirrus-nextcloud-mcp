// Package sheets implements the "sheets" tool group: reading and editing
// .xlsx files as tables whose first row is the header.
package sheets

import (
	"bytes"
	"context"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/extract"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	table "github.com/SantiPorres/cirrus-nextcloud-mcp/internal/sheets"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const (
	xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	defaultMaxRows  = 200
	maxRowsPerRead  = 2000

	saveWarning = "Make sure nobody has the file open in an online editor: a co-editing session can overwrite this " +
		"change after it is saved, and the API may show the old content until every editor session is closed."
)

type group struct {
	client *nextcloud.Client
	cfg    *config.Config
}

// Register adds the spreadsheet tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client, cfg: r.Config}

	editable := "No spreadsheet is editable until the operator sets NEXTCLOUD_EDITABLE_SPREADSHEETS."
	unavailable := "NEXTCLOUD_EDITABLE_SPREADSHEETS is not set"
	if len(r.Config.EditableSpreadsheets) > 0 {
		editable = "Only spreadsheets matching these patterns can be modified: " + strings.Join(r.Config.EditableSpreadsheets, ", ") + "."
		unavailable = ""
	}
	rules := " " + editable + " The file is edited in place, saved only if nobody changed it in the meantime, and read " +
		"again after saving (\"verified\"). Text starting with \"=\" is stored as text, never as a formula."

	tools.Add(r, tools.Spec{
		Name: "nc_sheet_read", Title: "Read spreadsheet table", Group: config.GroupSheets,
		Description: "Reads a sheet of an .xlsx file as a table: row 1 is the header and every other row becomes " +
			"{header: value}. Returns the headers, the sheet names and, when the sheet has an id column, \"next_id\" " +
			"(the next free id). Formulas are returned as their last calculated value and dates as ISO text. " +
			"Paged with offset/next_offset.",
	}, g.read)

	tools.Add(r, tools.Spec{
		Name: "nc_sheet_append_rows", Title: "Append spreadsheet rows", Group: config.GroupSheets, Permission: tools.Write,
		Unavailable: unavailable,
		Description: "Adds records at the end of a table, copying the format of the last record. Each row is " +
			"{header: value}; headers are matched ignoring case, and if any header does not exist nothing is " +
			"written. If the sheet has an id column and a row has no id, the next free one is assigned (see " +
			"\"ids_assigned\"); a duplicate id is refused. Read the sheet with nc_sheet_read first." + rules,
	}, g.appendRows)

	tools.Add(r, tools.Spec{
		Name: "nc_sheet_update_cells", Title: "Update spreadsheet cells", Group: config.GroupSheets, Permission: tools.Write,
		Destructive: true, Unavailable: unavailable,
		Description: "Changes cells of one existing record, found by the value of a key column (normally the id). " +
			"Exactly one row must match and the key column cannot be changed. Pass \"expected\" (current values of " +
			"some columns of that row, as read with nc_sheet_read) to make the change only if nobody edited them " +
			"since; otherwise it is refused and nothing is written." + rules,
	}, g.updateCells)

	tools.Add(r, tools.Spec{
		Name: "nc_sheet_create", Title: "Create spreadsheet sheet", Group: config.GroupSheets, Permission: tools.Write,
		Unavailable: unavailable,
		Description: "Creates a new sheet with a header row. If the .xlsx file does not exist, a new workbook with " +
			"that sheet is created (the parent folder must exist); if it exists, the sheet is added to it. An " +
			"existing sheet is never replaced." + rules,
	}, g.create)
}

// --- shared flow ------------------------------------------------------------

func (g *group) resolve(ctx context.Context, rawTarget string) (nextcloud.Target, error) {
	target, err := g.client.Resolve(ctx, rawTarget, true)
	if err != nil {
		return target, err
	}
	if !extract.IsSpreadsheet(target.Name) {
		return target, nextcloud.Errorf("%s is not an .xlsx file. Use nc_read_file for other formats.", target.Name)
	}
	return target, nil
}

func (g *group) checkEditable(target nextcloud.Target) error {
	if g.cfg.SpreadsheetEditable(target.Rel) {
		return nil
	}
	return nextcloud.Errorf("%s cannot be modified. Editable spreadsheets: %s.", target.Rel, strings.Join(g.cfg.EditableSpreadsheets, ", "))
}

func openWorkbook(data []byte) (*excelize.File, error) {
	workbook, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nextcloud.Errorf("Could not open the spreadsheet: the file is damaged or password-protected.")
	}
	return workbook, nil
}

func (g *group) openSheet(workbook *excelize.File, name string, forWriting bool) (*table.Sheet, error) {
	sheet, err := table.Open(workbook, name)
	if err != nil {
		return nil, err
	}
	if forWriting && g.cfg.SheetProtected(sheet.Name) {
		return nil, nextcloud.Errorf("The sheet %q is protected by the server configuration and cannot be modified.", sheet.Name)
	}
	return sheet, nil
}

// edit is the shared flow of the tools that modify an existing workbook:
// download with the ETag, change in memory, save with If-Match, then
// download again and let verify confirm the change is really there.
func (g *group) edit(ctx context.Context, rawTarget string,
	operate func(workbook *excelize.File) error, verify func(saved *excelize.File) bool,
) (nextcloud.Target, bool, error) {
	target, err := g.resolve(ctx, rawTarget)
	if err != nil {
		return target, false, err
	}
	if err := g.checkEditable(target); err != nil {
		return target, false, err
	}
	data, etag, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return target, false, err
	}
	workbook, err := openWorkbook(data)
	if err != nil {
		return target, false, err
	}
	defer func() { _ = workbook.Close() }()
	if err := operate(workbook); err != nil {
		return target, false, err
	}
	verified, err := g.save(ctx, target, workbook, nextcloud.PutCondition{IfMatch: etag}, verify)
	return target, verified, err
}

func (g *group) save(ctx context.Context, target nextcloud.Target, workbook *excelize.File,
	cond nextcloud.PutCondition, verify func(saved *excelize.File) bool,
) (bool, error) {
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return false, nextcloud.Errorf("Could not build the spreadsheet file. Nothing was saved.")
	}
	if int64(buffer.Len()) > g.cfg.MaxDownloadBytes {
		return false, nextcloud.Errorf("The spreadsheet would be too large (%d bytes, limit %d). Nothing was saved.", buffer.Len(), g.cfg.MaxDownloadBytes)
	}
	if _, err := g.client.Put(ctx, target, buffer.Bytes(), xlsxContentType, cond); err != nil {
		return false, err
	}

	// The write succeeded. A failed read-back is reported as "not verified", not as an error.
	data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return false, nil
	}
	saved, err := openWorkbook(data)
	if err != nil {
		return false, nil
	}
	defer func() { _ = saved.Close() }()
	return verify(saved), nil
}

// --- tools ------------------------------------------------------------------

type readInput struct {
	Target   string `json:"target" jsonschema:"Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	Sheet    string `json:"sheet,omitempty" jsonschema:"Sheet name. Defaults to the first sheet."`
	Offset   int    `json:"offset,omitempty" jsonschema:"Number of records to skip. Defaults to 0."`
	MaxRows  int    `json:"max_rows,omitempty" jsonschema:"Maximum number of records to return (1-2000). Defaults to 200."`
	IDColumn string `json:"id_column,omitempty" jsonschema:"Header of the identifier column, used for next_id. Defaults to \"ID\"."`
}

func (g *group) read(ctx context.Context, in readInput) (any, error) {
	target, err := g.resolve(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return nil, err
	}
	workbook, err := openWorkbook(data)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workbook.Close() }()
	sheet, err := g.openSheet(workbook, in.Sheet, false)
	if err != nil {
		return nil, err
	}
	maxRows := in.MaxRows
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	page := sheet.Read(in.Offset, min(maxRows, maxRowsPerRead), in.IDColumn)
	out := map[string]any{
		"path": target.Rel, "filename": target.Name, "sheets": table.SheetNames(workbook),
		"sheet": page.Sheet, "headers": page.Headers, "total_rows": page.TotalRows,
		"returned_rows": page.ReturnedRows, "last_row_number": page.LastRowNumber, "rows": page.Rows,
		"editable": g.cfg.AllowWrite && g.cfg.SpreadsheetEditable(target.Rel) && !g.cfg.SheetProtected(page.Sheet),
	}
	if page.NextOffset != nil {
		out["next_offset"] = *page.NextOffset
	}
	if page.NextID != nil {
		out["next_id"] = page.NextID
	}
	return out, nil
}

type appendInput struct {
	Target   string           `json:"target" jsonschema:"Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	Rows     []map[string]any `json:"rows" jsonschema:"The records to add, each an object {header: value}. Values are text, numbers or booleans."`
	Sheet    string           `json:"sheet,omitempty" jsonschema:"Sheet name. Defaults to the first sheet."`
	IDColumn string           `json:"id_column,omitempty" jsonschema:"Header of the identifier column. Defaults to \"ID\"."`
}

func (g *group) appendRows(ctx context.Context, in appendInput) (any, error) {
	var result table.Appended
	target, verified, err := g.edit(ctx, in.Target,
		func(workbook *excelize.File) error {
			sheet, err := g.openSheet(workbook, in.Sheet, true)
			if err != nil {
				return err
			}
			result, err = sheet.Append(in.Rows, in.IDColumn)
			return err
		},
		func(saved *excelize.File) bool {
			sheet, err := table.Open(saved, in.Sheet)
			return err == nil && sheet.LastDataRow() >= result.FirstRowNumber+result.RowsAdded-1
		})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path": target.Rel, "filename": target.Name, "rows_added": result.RowsAdded,
		"first_row_number": result.FirstRowNumber, "ids_assigned": result.IDsAssigned,
		"verified": verified, "warning": saveWarning,
	}, nil
}

type updateInput struct {
	Target    string         `json:"target" jsonschema:"Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	KeyColumn string         `json:"key_column" jsonschema:"Header that identifies the record, e.g. \"ID\"."`
	KeyValue  any            `json:"key_value" jsonschema:"Value of the key column in the record to change, e.g. \"D-12\" or 7."`
	Changes   map[string]any `json:"changes" jsonschema:"{header: new value}. An empty string clears the cell."`
	Expected  map[string]any `json:"expected,omitempty" jsonschema:"Optional {header: current value} that must still hold for the change to be made."`
	Sheet     string         `json:"sheet,omitempty" jsonschema:"Sheet name. Defaults to the first sheet."`
}

func (g *group) updateCells(ctx context.Context, in updateInput) (any, error) {
	var result table.Updated
	target, verified, err := g.edit(ctx, in.Target,
		func(workbook *excelize.File) error {
			sheet, err := g.openSheet(workbook, in.Sheet, true)
			if err != nil {
				return err
			}
			result, err = sheet.Update(in.KeyColumn, in.KeyValue, in.Changes, in.Expected)
			return err
		},
		func(saved *excelize.File) bool {
			sheet, err := table.Open(saved, in.Sheet)
			if err != nil {
				return false
			}
			// A row whose cells are all empty is not returned; its values then compare as "".
			values := map[string]any{}
			if page := sheet.Read(result.RowNumber-2, 1, ""); len(page.Rows) == 1 && page.Rows[0].Row == result.RowNumber {
				values = page.Rows[0].Values
			}
			for header, change := range result.Changed {
				if !table.Same(values[header], change.After) {
					return false
				}
			}
			return true
		})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path": target.Rel, "filename": target.Name, "row_number": result.RowNumber, "changed": result.Changed,
		"verified": verified, "warning": saveWarning,
	}, nil
}

type createInput struct {
	Target  string   `json:"target" jsonschema:"Path of the .xlsx file relative to the top folder. It is created if it does not exist."`
	Sheet   string   `json:"sheet" jsonschema:"Name of the new sheet (at most 31 characters)."`
	Headers []string `json:"headers" jsonschema:"Column names for row 1."`
}

func (g *group) create(ctx context.Context, in createInput) (any, error) {
	sheetName := strings.TrimSpace(in.Sheet)
	if sheetName == "" {
		return nil, nextcloud.Errorf("sheet must not be empty.")
	}
	if g.cfg.SheetProtected(sheetName) {
		return nil, nextcloud.Errorf("The sheet name %q is protected by the server configuration.", sheetName)
	}
	verify := func(saved *excelize.File) bool {
		sheet, err := table.Open(saved, sheetName)
		return err == nil && len(sheet.Headers()) == len(in.Headers)
	}

	target, err := g.resolve(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	if err := g.checkEditable(target); err != nil {
		return nil, err
	}
	_, statErr := g.client.Stat(ctx, target)
	switch {
	case nextcloud.IsNotFound(statErr):
		if !strings.HasSuffix(strings.ToLower(target.Name), ".xlsx") {
			return nil, nextcloud.Errorf("New workbooks must have the .xlsx extension.")
		}
		workbook, err := table.NewWorkbook(sheetName, in.Headers)
		if err != nil {
			return nil, err
		}
		defer func() { _ = workbook.Close() }()
		verified, err := g.save(ctx, target, workbook, nextcloud.PutCondition{CreateOnly: true}, verify)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": target.Rel, "sheet": sheetName, "status": "workbook created", "verified": verified}, nil
	case statErr != nil:
		return nil, statErr
	}

	target, verified, err := g.edit(ctx, in.Target,
		func(workbook *excelize.File) error { return table.AddSheet(workbook, sheetName, in.Headers) }, verify)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path": target.Rel, "sheet": sheetName, "status": "sheet added", "verified": verified, "warning": saveWarning,
	}, nil
}
