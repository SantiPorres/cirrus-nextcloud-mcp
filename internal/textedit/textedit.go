// Package textedit holds the pure text operations behind the file editing
// tools: exact replacement, line-range edits, content hashes and structural
// validation. It has no network access.
package textedit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Error is a user-facing problem with an edit. The file is left untouched.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func errorf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// Hash is a short fingerprint of a text that ignores the line-ending style.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(text, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// UniformCRLF reports whether every line break in text is "\r\n". Such files
// are edited as "\n" and converted back when saved; files that mix styles
// are edited as they are.
func UniformCRLF(text string) bool {
	crlf := strings.Count(text, "\r\n")
	return crlf > 0 && crlf == strings.Count(text, "\n")
}

// SplitLines splits on "\n" only and reports whether the text ended with a
// line break. An empty text has no lines.
func SplitLines(text string) (lines []string, trailing bool) {
	lines = strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}
	return lines, false
}

// Numbered renders lines as "     5| text", starting at firstNumber.
func Numbered(lines []string, firstNumber int) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d| %s", firstNumber+i, strings.TrimRight(line, "\r"))
	}
	return b.String()
}

// Validate refuses content that would be structurally broken for its file
// type: XML (and draw.io) must be well formed, JSON must parse.
func Validate(filename, text string) error {
	extension := strings.ToLower(path.Ext(filename))
	var problem error
	switch extension {
	case ".xml", ".drawio", ".dio":
		problem = checkXML(text)
	case ".json":
		if !json.Valid([]byte(text)) {
			var probe any
			problem = json.Unmarshal([]byte(text), &probe)
		}
	}
	if problem != nil {
		return errorf("The result would not be valid %s (%v). Nothing was saved.", strings.ToUpper(extension[1:]), problem)
	}
	return nil
}

func checkXML(text string) error {
	decoder := xml.NewDecoder(strings.NewReader(text))
	elements := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if _, ok := token.(xml.StartElement); ok {
			elements++
		}
	}
	if elements == 0 {
		return errors.New("no root element")
	}
	return nil
}

// ReplaceResult describes what Replace did.
type ReplaceResult struct {
	Text         string
	Replacements int
	// Matched is "exact" or "ignoring trailing whitespace".
	Matched string
}

// Replace substitutes oldText with newText. It matches exactly first; when
// nothing matches it retries ignoring spaces and tabs at the end of each
// line. More than one match is refused unless replaceAll is set. Line
// endings are the caller's business: pass "\n" text.
func Replace(text, oldText, newText string, replaceAll bool) (ReplaceResult, error) {
	checkUnique := func(count int) error {
		if count > 1 && !replaceAll {
			return errorf("old_text appears %d times. Include more surrounding text to make it unique, or pass replace_all=true.", count)
		}
		return nil
	}

	if count := strings.Count(text, oldText); count > 0 {
		if err := checkUnique(count); err != nil {
			return ReplaceResult{}, err
		}
		return ReplaceResult{Text: strings.ReplaceAll(text, oldText, newText), Replacements: count, Matched: "exact"}, nil
	}

	lines := strings.Split(oldText, "\n")
	for i, line := range lines {
		lines[i] = regexp.QuoteMeta(strings.TrimRight(line, " \t\r"))
	}
	pattern, err := regexp.Compile(strings.Join(lines, "[ \t]*\n"))
	if err != nil {
		return ReplaceResult{}, errorf("old_text could not be used as a search text.")
	}
	matches := pattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return ReplaceResult{}, errorf("old_text was not found in the file. Read the file again and copy the text exactly.")
	}
	if err := checkUnique(len(matches)); err != nil {
		return ReplaceResult{}, err
	}
	return ReplaceResult{
		Text:         pattern.ReplaceAllLiteralString(text, newText),
		Replacements: len(matches),
		Matched:      "ignoring trailing whitespace",
	}, nil
}

// LineEdit describes what ReplaceLines did.
type LineEdit struct {
	Text          string
	LinesReplaced int
	LinesInserted int
	TotalLines    int
	// Preview shows the edited area with line numbers.
	Preview string
}

const (
	previewContext  = 2
	previewMaxLines = 30
)

// ReplaceLines replaces lines startLine..endLine (1-based, inclusive) of a
// "\n" text with newText. endLine = startLine-1 inserts before startLine;
// startLine = total+1 appends. An empty newText deletes the lines.
func ReplaceLines(text string, startLine, endLine int, newText string) (LineEdit, error) {
	lines, trailing := SplitLines(text)
	total := len(lines)
	if startLine < 1 || startLine > total+1 {
		return LineEdit{}, errorf("start_line must be between 1 and %d (the file has %d lines).", total+1, total)
	}
	if endLine < startLine-1 || endLine > total {
		return LineEdit{}, errorf("end_line must be between %d and %d.", startLine-1, total)
	}

	newText = strings.ReplaceAll(newText, "\r\n", "\n")
	var newLines []string
	if newText != "" {
		newLines = strings.Split(newText, "\n")
		if strings.HasSuffix(newText, "\n") {
			newLines = newLines[:len(newLines)-1]
		}
	}

	result := make([]string, 0, total-(endLine-startLine+1)+len(newLines))
	result = append(result, lines[:startLine-1]...)
	result = append(result, newLines...)
	result = append(result, lines[endLine:]...)

	updated := strings.Join(result, "\n")
	if len(result) > 0 && (trailing || total == 0) {
		updated += "\n"
	}

	first := max(1, startLine-previewContext)
	last := min(len(result), startLine-1+len(newLines)+previewContext, first+previewMaxLines-1)
	preview := ""
	if last >= first {
		preview = Numbered(result[first-1:last], first)
	}
	return LineEdit{
		Text:          updated,
		LinesReplaced: endLine - startLine + 1,
		LinesInserted: len(newLines),
		TotalLines:    len(result),
		Preview:       preview,
	}, nil
}
