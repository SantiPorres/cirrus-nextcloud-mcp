package extract

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// maxXMLPartBytes bounds how much of one XML part of an Office file is read.
const maxXMLPartBytes = 128 << 20

func zipPart(archive *zip.Reader, name string) (*node, error) {
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = reader.Close() }()
		return parseXML(io.LimitReader(reader, maxXMLPartBytes))
	}
	return nil, errors.New("missing " + name)
}

// docxToText renders the body of a Word document: headings as "#" lines,
// paragraphs separated by blank lines and tables as "| a | b |" rows.
func docxToText(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("not a valid .docx file")
	}
	document, err := zipPart(archive, "word/document.xml")
	if err != nil {
		return "", errors.New("not a valid .docx file")
	}
	body := document.child("w:body")
	if body == nil {
		return "", errors.New("not a valid .docx file")
	}

	// Heading styles are recognised by name ("heading 1"), not by id, because
	// ids are localised.
	headingLevel := map[string]int{}
	if styles, err := zipPart(archive, "word/styles.xml"); err == nil {
		styles.walk(func(n *node) bool {
			if n.name != "w:style" {
				return true
			}
			if name := n.child("w:name"); name != nil {
				lower := strings.ToLower(name.attr("w:val"))
				if rest, ok := strings.CutPrefix(lower, "heading"); ok {
					level, err := strconv.Atoi(strings.TrimSpace(rest))
					if err != nil || level < 1 {
						level = 1
					}
					headingLevel[n.attr("w:styleId")] = min(level, 6)
				}
			}
			return false
		})
	}

	var parts []string
	var block func(container *node)
	block = func(container *node) {
		for _, child := range container.children {
			switch child.name {
			case "w:p":
				text := strings.TrimSpace(paragraphText(child))
				if text == "" {
					continue
				}
				if pPr := child.child("w:pPr"); pPr != nil {
					if style := pPr.child("w:pStyle"); style != nil {
						if level := headingLevel[style.attr("w:val")]; level > 0 {
							text = strings.Repeat("#", level) + " " + text
						}
					}
				}
				parts = append(parts, text)
			case "w:tbl":
				if table := tableText(child); table != "" {
					parts = append(parts, table)
				}
			case "w:sdt":
				if content := child.child("w:sdtContent"); content != nil {
					block(content)
				}
			}
		}
	}
	block(body)
	return strings.Join(parts, "\n\n"), nil
}

func paragraphText(p *node) string {
	var b strings.Builder
	p.walk(func(n *node) bool {
		switch n.name {
		case "w:t":
			b.WriteString(n.innerText())
		case "w:tab":
			b.WriteByte('\t')
		case "w:br", "w:cr":
			b.WriteByte('\n')
		case "w:pPr", "w:rPr", "w:del", "w:instrText":
			return false
		}
		return true
	})
	return b.String()
}

func tableText(tbl *node) string {
	var rows [][]string
	for _, tr := range tbl.children {
		if tr.name != "w:tr" {
			continue
		}
		var cells []string
		for _, tc := range tr.children {
			if tc.name != "w:tc" {
				continue
			}
			var paragraphs []string
			tc.walk(func(n *node) bool {
				if n.name == "w:p" {
					paragraphs = append(paragraphs, paragraphText(n))
					return false
				}
				return true
			})
			cells = append(cells, strings.TrimSpace(strings.Join(paragraphs, "\n")))
		}
		rows = append(rows, cells)
	}
	return tableToText(rows)
}

// tableToText renders rows as "| a | b |" lines.
func tableToText(rows [][]string) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = strings.ReplaceAll(cell, "\n", " ")
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
	}
	return strings.Join(lines, "\n")
}
