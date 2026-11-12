package extract

import (
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/samples"
)

func TestPDF(t *testing.T) {
	text, err := Text("report.PDF", samples.PDF("Hello Nextcloud", "Second page"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- Page 1 ---", "Hello Nextcloud", "--- Page 2 ---", "Second page"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestBrokenPDFIsAnError(t *testing.T) {
	for _, data := range []string{"", "not a pdf at all", "%PDF-1.4\n1 0 obj\n<< /Type"} {
		_, err := Text("x.pdf", []byte(data))
		var invalid *InvalidError
		if !errors.As(err, &invalid) {
			t.Errorf("%q: expected InvalidError, got %v", data, err)
		}
	}
}

func TestDocx(t *testing.T) {
	body := `<w:p><w:pPr><w:pStyle w:val="Ttulo1"/></w:pPr><w:r><w:t>Title</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="H2"/></w:pPr><w:r><w:t>Sub</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Hello </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>world</w:t><w:tab/><w:t>!</w:t></w:r></w:p>` +
		`<w:p></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p><w:p><w:r><w:t>more</w:t></w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:p><w:r><w:t>A2</w:t></w:r></w:p></w:tc><w:tc><w:p/></w:tc></w:tr></w:tbl>` +
		`<w:sdt><w:sdtContent><w:p><w:r><w:t>Inside control</w:t></w:r></w:p></w:sdtContent></w:sdt>`
	text, err := Text("doc.docx", samples.Docx(body))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Title\n\n## Sub\n\nHello world\t!\n\n| A1 | B1 more |\n| A2 |  |\n\nInside control"
	if text != want {
		t.Errorf("got:\n%q\nwant:\n%q", text, want)
	}
}

func TestBrokenDocx(t *testing.T) {
	for _, data := range [][]byte{[]byte("nope"), samples.Zip(map[string]string{"other.txt": "x"})} {
		_, err := Text("a.docx", data)
		var invalid *InvalidError
		if !errors.As(err, &invalid) {
			t.Errorf("expected InvalidError, got %v", err)
		}
	}
}

func TestXlsx(t *testing.T) {
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	_ = workbook.SetSheetName("Sheet1", "Data")
	_ = workbook.SetSheetRow("Data", "A1", &[]any{"ID", "Topic"})
	_ = workbook.SetSheetRow("Data", "A2", &[]any{1, "First\nline"})
	_ = workbook.SetCellFormula("Data", "A4", "=1+1")
	_, _ = workbook.NewSheet("Empty")
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	text, err := Text("book.xlsx", buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "## Sheet: Data\n| ID | Topic |\n| 1 | First line |\n\n## Sheet: Empty\n(empty sheet)"
	if text != want {
		t.Errorf("got:\n%q\nwant:\n%q", text, want)
	}
	if _, err := Text("bad.xlsx", []byte("nope")); err == nil {
		t.Error("a broken spreadsheet must be an error")
	}
}

func TestDrawio(t *testing.T) {
	graph := `<mxGraphModel dx="10"><root><mxCell id="0"/><mxCell id="1" value="A &amp; B &lt;br&gt;" parent="0"/></root></mxGraphModel>`
	decoded := DecodeDrawio(samples.CompressedDrawio(graph))
	want := `<mxfile host="app.diagrams.net">
 <diagram id="d1" name="Page-1">
  <mxGraphModel dx="10">
   <root>
    <mxCell id="0" />
    <mxCell id="1" value="A &amp; B &lt;br&gt;" parent="0" />
   </root>
  </mxGraphModel>
 </diagram>
</mxfile>`
	if decoded != want {
		t.Errorf("got:\n%s\nwant:\n%s", decoded, want)
	}
	// Decoding is stable: the expanded form is returned untouched.
	if again := DecodeDrawio(decoded); again != decoded {
		t.Error("an uncompressed file must be returned as is")
	}
	for _, untouched := range []string{"not xml", `<mxfile><diagram>!!not-base64!!</diagram></mxfile>`, `<mxfile><diagram/></mxfile>`} {
		if DecodeDrawio(untouched) != untouched {
			t.Errorf("%q should be returned untouched", untouched)
		}
	}
	text, err := Text("d.drawio", []byte(samples.CompressedDrawio(graph)))
	if err != nil || text != want {
		t.Errorf("Text: %v", err)
	}
}

func TestPlainTextAndUnsupported(t *testing.T) {
	text, err := Text("notes.md", []byte("# Título\n"))
	if err != nil || text != "# Título\n" {
		t.Fatalf("%q %v", text, err)
	}
	if text, err := Text("Makefile", []byte("all:\n")); err != nil || text != "all:\n" {
		t.Errorf("UTF-8 files of unknown type should be readable: %v", err)
	}
	_, err = Text("photo.jpg", []byte{0xff, 0xd8, 0xff, 0x00, 0x10})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Extension != ".jpg" {
		t.Errorf("expected UnsupportedError, got %v", err)
	}
}
