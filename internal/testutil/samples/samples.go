// Package samples builds small documents in memory for tests.
package samples

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// PDF returns a one-page-per-text PDF with the given lines of text.
func PDF(pages ...string) []byte {
	var objects []string
	kids := make([]string, len(pages))
	// Object numbers: 1 catalog, 2 pages, 3 font, then (page, content) pairs.
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
	for i, text := range pages {
		escaped := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(text)
		stream := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", escaped)
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}

// Zip builds an archive from name -> content.
func Zip(files map[string]string) []byte {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		part, err := writer.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// Docx builds a Word document whose body is the given WordprocessingML.
func Docx(bodyXML string) []byte {
	return Zip(map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document ` + wordNS + `><w:body>` + bodyXML + `</w:body></w:document>`,
		"word/styles.xml": `<?xml version="1.0"?><w:styles ` + wordNS + `>` +
			`<w:style w:type="paragraph" w:styleId="Ttulo1"><w:name w:val="heading 1"/></w:style>` +
			`<w:style w:type="paragraph" w:styleId="H2"><w:name w:val="Heading 2"/></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
			`</w:styles>`,
	})
}

// CompressedDrawio builds a draw.io file whose diagram is stored compressed.
func CompressedDrawio(graphXML string) string {
	var deflated bytes.Buffer
	writer, err := flate.NewWriter(&deflated, flate.DefaultCompression)
	if err != nil {
		panic(err)
	}
	if _, err := writer.Write([]byte(url.PathEscape(graphXML))); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return `<mxfile host="app.diagrams.net"><diagram id="d1" name="Page-1">` +
		base64.StdEncoding.EncodeToString(deflated.Bytes()) + `</diagram></mxfile>`
}
