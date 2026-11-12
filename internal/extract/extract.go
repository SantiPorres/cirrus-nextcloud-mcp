// Package extract turns the bytes of a document into plain text for the
// model: PDF, Word, Excel, draw.io diagrams and text files.
package extract

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// MaxSheetRows is how many rows of each sheet Text renders for a spreadsheet.
const MaxSheetRows = 500

// UnsupportedError is returned for file types that cannot be read as text.
type UnsupportedError struct{ Extension string }

func (e *UnsupportedError) Error() string {
	extension := e.Extension
	if extension == "" {
		extension = "(none)"
	}
	return fmt.Sprintf("Unsupported file type %s. Supported: PDF, DOCX, XLSX, draw.io and text files.", extension)
}

// InvalidError is returned when a file of a supported type cannot be parsed.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// IsDrawio reports whether the name has a draw.io extension.
func IsDrawio(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".drawio", ".dio":
		return true
	}
	return false
}

// IsSpreadsheet reports whether the name has an Excel extension.
func IsSpreadsheet(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".xlsx", ".xlsm":
		return true
	}
	return false
}

// DecodeText converts bytes to a string, replacing invalid UTF-8.
func DecodeText(data []byte) string {
	return strings.ToValidUTF8(string(data), "�")
}

// Text extracts the text of a file, choosing the method from its extension.
// Files of an unknown type are accepted when they are valid UTF-8 text.
func Text(filename string, data []byte) (string, error) {
	extension := strings.ToLower(path.Ext(filename))
	switch {
	case extension == ".pdf":
		return pdfToText(data)
	case extension == ".docx":
		text, err := docxToText(data)
		if err != nil {
			return "", &InvalidError{Message: "Could not read the Word document: " + err.Error() + "."}
		}
		return text, nil
	case IsSpreadsheet(filename):
		return xlsxToText(data)
	case IsDrawio(filename):
		return DecodeDrawio(DecodeText(data)), nil
	case utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		return string(data), nil
	}
	return "", &UnsupportedError{Extension: extension}
}

func pdfToText(data []byte) (text string, err error) {
	// The PDF library panics on some malformed files.
	defer func() {
		if recover() != nil {
			text, err = "", &InvalidError{Message: "Could not read the PDF: the file is damaged or uses an unsupported feature."}
		}
	}()
	reader, openErr := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if openErr != nil {
		return "", &InvalidError{Message: "Could not read the PDF: the file is damaged or encrypted."}
	}
	var pages []string
	for number := 1; number <= reader.NumPage(); number++ {
		page := reader.Page(number)
		if page.V.IsNull() {
			continue
		}
		content, pageErr := page.GetPlainText(nil)
		if pageErr != nil {
			continue
		}
		if content = strings.TrimSpace(content); content != "" {
			pages = append(pages, fmt.Sprintf("--- Page %d ---\n%s", number, content))
		}
	}
	if len(pages) == 0 {
		return "(No extractable text. The PDF is probably scanned images and would need OCR.)", nil
	}
	return strings.Join(pages, "\n\n"), nil
}

func xlsxToText(data []byte) (string, error) {
	workbook, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", &InvalidError{Message: "Could not read the spreadsheet: the file is damaged or protected."}
	}
	defer func() { _ = workbook.Close() }()

	var parts []string
	for _, sheet := range workbook.GetSheetList() {
		rows, err := workbook.Rows(sheet)
		if err != nil {
			continue // chart sheets and the like
		}
		var table [][]string
		truncated := false
		for index := 0; rows.Next(); index++ {
			if index >= MaxSheetRows {
				truncated = true
				break
			}
			cells, err := rows.Columns()
			if err != nil {
				return "", errors.Join(&InvalidError{Message: "Could not read the spreadsheet."}, rows.Close())
			}
			empty := true
			for _, cell := range cells {
				if cell != "" {
					empty = false
					break
				}
			}
			if !empty {
				table = append(table, cells)
			}
		}
		_ = rows.Close()
		body := "(empty sheet)"
		if len(table) > 0 {
			body = tableToText(table)
		}
		if truncated {
			body += fmt.Sprintf("\n(showing first %d rows)", MaxSheetRows)
		}
		parts = append(parts, "## Sheet: "+sheet+"\n"+body)
	}
	return strings.Join(parts, "\n\n"), nil
}
