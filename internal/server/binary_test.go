package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/pdfrender"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/samples"
)

func pngImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// binaryCall runs a tool that returns a JSON description followed by binary content.
func (h *harness) binaryCall(name string, arguments map[string]any) (map[string]any, []mcp.Content) {
	h.t.Helper()
	result := h.raw(name, arguments)
	if result.IsError {
		h.t.Fatalf("%s failed: %s", name, text(result))
	}
	var description map[string]any
	first, ok := result.Content[0].(*mcp.TextContent)
	if !ok || json.Unmarshal([]byte(first.Text), &description) != nil {
		h.t.Fatalf("%s: the first content is not a JSON description", name)
	}
	if strings.Contains(first.Text, "base64") || len(first.Text) > 2000 {
		h.t.Errorf("%s: binary data leaked into the text the model reads: %.200s", name, first.Text)
	}
	return description, result.Content[1:]
}

func TestBinaryToolAvailability(t *testing.T) {
	names := newHarness(t, nil).toolNames()
	for _, name := range []string{"nc_download", "nc_read_image"} {
		if !slices.Contains(names, name) {
			t.Errorf("%s should be available", name)
		}
	}
	for _, name := range []string{"nc_upload_start", "nc_upload_chunk", "nc_upload_finish"} {
		if slices.Contains(names, name) {
			t.Errorf("%s must not exist on a read-only server", name)
		}
	}
	if slices.Contains(names, "nc_render_pdf") != (pdfrender.Find() != "") {
		t.Error("nc_render_pdf must exist exactly when pdftoppm is installed")
	}
}

func TestDownloadWholeAndInParts(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_MAX_EMBED_BYTES": "20", "NEXTCLOUD_MAX_CHUNK_BYTES": "8"})
	small := []byte{0x00, 0xff, 0x10, 'a', 'b'}
	h.fake.AddFile("/Bin/small file.bin", small)
	big := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	h.fake.AddFile("/big.pdf", big)

	description, content := h.binaryCall("nc_download", args("target", "/Bin/small file.bin"))
	resource := content[0].(*mcp.EmbeddedResource).Resource
	sum := sha256.Sum256(small)
	if !bytes.Equal(resource.Blob, small) || resource.URI != "nextcloud:///Bin/small%20file.bin" || description["complete"] != true ||
		description["sha256"] != hex.EncodeToString(sum[:]) || description["total_bytes"].(float64) != 5 {
		t.Errorf("description = %v, uri = %s", description, resource.URI)
	}

	h.fails("nc_download", args("target", "/big.pdf"), "too large to return in one piece")
	h.fails("nc_download", args("target", "/big.pdf", "offset", 0, "length", 9), "at most 8 bytes")

	var assembled []byte
	var etag any
	offset := 0.0
	for range 10 {
		arguments := args("target", "/big.pdf", "offset", offset, "length", 8)
		if etag != nil {
			arguments["etag"] = etag
		}
		description, content = h.binaryCall("nc_download", arguments)
		assembled = append(assembled, content[0].(*mcp.EmbeddedResource).Resource.Blob...)
		etag = description["etag"]
		if description["mime_type"] != "application/pdf" {
			t.Errorf("mime = %v", description["mime_type"])
		}
		next, more := description["next_offset"].(float64)
		if !more {
			break
		}
		offset = next
	}
	if !bytes.Equal(assembled, big) {
		t.Errorf("assembled = %q", assembled)
	}

	// A change between two parts is detected through the etag.
	h.fake.AddFile("/big.pdf", []byte("completely different content, longer than before"))
	h.fails("nc_download", args("target", "/big.pdf", "offset", 8, "length", 8, "etag", etag), "changed while it was being downloaded")
	h.fails("nc_download", args("target", "/big.pdf", "offset", 5000, "length", 8), "past the end")
	h.fails("nc_download", args("target", "/big.pdf", "offset", -1), "negative")
	h.fails("nc_download", args("target", "/Bin"), "is a folder")
	h.fails("nc_download", args("target", "/../x"), "'..'")
}

func TestChunkedUploadOnlyCreates(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_MAX_CHUNK_BYTES": "6", "NEXTCLOUD_MAX_UPLOAD_BYTES": "20"})
	h.fake.AddDir("/Scans")
	h.fake.AddFile("/Scans/taken.bin", []byte("original"))
	encode := base64.StdEncoding.EncodeToString

	started := h.call("nc_upload_start", args("path", "/Scans/new.bin", "total_size", 11))
	id := started["upload_id"].(string)
	if !strings.HasPrefix(id, "cirrus-") || started["max_chunk_bytes"].(float64) != 6 {
		t.Fatalf("started = %v", started)
	}
	// Pieces may arrive out of order, and a piece can be sent again.
	h.call("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 2, "data_base64", encode([]byte("world"))))
	h.fails("nc_upload_finish", args("upload_id", id, "path", "/Scans/new.bin"), "Piece 1 is missing")
	h.call("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 1, "data_base64", encode([]byte("WRONG "))))
	h.call("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 1, "data_base64", encode([]byte("hello "))))
	h.fails("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 3, "data_base64", encode([]byte("7 bytes"))), "too large")
	h.fails("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 3, "data_base64", "***"), "not valid base64")
	h.fails("nc_upload_chunk", args("upload_id", id, "path", "/Scans/new.bin", "index", 0, "data_base64", encode([]byte("x"))), "between 1 and")
	h.fails("nc_upload_chunk", args("upload_id", "../../files/alice/x", "path", "/Scans/new.bin", "index", 1, "data_base64", encode([]byte("x"))), "upload_id is not valid")

	sum := sha256.Sum256([]byte("hello world"))
	finished := h.call("nc_upload_finish", args("upload_id", id, "path", "/Scans/new.bin", "sha256", hex.EncodeToString(sum[:])))
	if finished["status"] != "created" || finished["verified"] != true || finished["sha256_matches"] != true ||
		finished["size_bytes"].(float64) != 11 || h.file("/Scans/new.bin") != "hello world" {
		t.Errorf("finished = %v", finished)
	}

	// Never onto an existing file: refused at start, and again at finish if the file appeared meanwhile.
	h.fails("nc_upload_start", args("path", "/Scans/taken.bin"), "already exists")
	h.fails("nc_upload_start", args("path", "/Scans/huge.bin", "total_size", 21), "too large to upload")
	h.fails("nc_upload_start", args("path", "/../x.bin"), "'..'")
	race := h.call("nc_upload_start", args("path", "/Scans/race.bin"))["upload_id"]
	h.call("nc_upload_chunk", args("upload_id", race, "path", "/Scans/race.bin", "index", 1, "data_base64", encode([]byte("mine"))))
	h.fake.AddFile("/Scans/race.bin", []byte("theirs"))
	h.fails("nc_upload_finish", args("upload_id", race, "path", "/Scans/race.bin"), "already exists")
	// ... and the server itself refuses it too, should the file appear after our check.
	late := h.call("nc_upload_start", args("path", "/Scans/late.bin"))["upload_id"]
	h.call("nc_upload_chunk", args("upload_id", late, "path", "/Scans/late.bin", "index", 1, "data_base64", encode([]byte("mine"))))
	h.fails("nc_upload_finish", args("upload_id", late, "path", "/Scans/taken.bin"), "already exists")
	if h.file("/Scans/race.bin") != "theirs" || h.file("/Scans/taken.bin") != "original" {
		t.Error("an upload replaced an existing file")
	}

	// The total size limit is enforced on what was actually sent.
	big := h.call("nc_upload_start", args("path", "/Scans/big.bin"))["upload_id"]
	for index := 1; index <= 4; index++ {
		h.call("nc_upload_chunk", args("upload_id", big, "path", "/Scans/big.bin", "index", index, "data_base64", encode([]byte("123456"))))
	}
	h.fails("nc_upload_finish", args("upload_id", big, "path", "/Scans/big.bin"), "too large to upload")
	if h.fake.Exists("/Scans/big.bin") {
		t.Error("an oversized upload was written")
	}

	mismatch := h.call("nc_upload_start", args("path", "/Scans/sum.bin"))["upload_id"]
	h.call("nc_upload_chunk", args("upload_id", mismatch, "path", "/Scans/sum.bin", "index", 1, "data_base64", encode([]byte("data"))))
	if result := h.call("nc_upload_finish", args("upload_id", mismatch, "path", "/Scans/sum.bin", "sha256", strings.Repeat("0", 64))); result["sha256_matches"] != false || result["verified"] != false {
		t.Errorf("a wrong checksum must be reported: %v", result)
	}
	h.assertNoBlindOverwrite()
}

func TestReadImage(t *testing.T) {
	h := newHarness(t, nil)
	small := pngImage(t, 30, 20)
	h.fake.AddFile("/Pics/small.png", small)
	h.fake.AddFile("/Pics/large.png", pngImage(t, 600, 300))
	heic := h.fake.AddFile("/Pics/photo.heic", []byte("not decodable here"))
	h.fake.AddFile("/Pics/notes.txt", []byte("just text"))

	description, content := h.binaryCall("nc_read_image", args("target", "/Pics/small.png"))
	picture := content[0].(*mcp.ImageContent)
	if !bytes.Equal(picture.Data, small) || picture.MIMEType != "image/png" || description["resized"] != false || description["source"] != "file" {
		t.Errorf("small = %v", description)
	}

	description, content = h.binaryCall("nc_read_image", args("target", "/Pics/large.png", "max_side", 100))
	picture = content[0].(*mcp.ImageContent)
	config, _, err := image.DecodeConfig(bytes.NewReader(picture.Data))
	if err != nil || config.Width != 100 || config.Height != 50 || description["resized"] != true ||
		description["original_width"].(float64) != 600 || picture.MIMEType != "image/jpeg" {
		t.Errorf("large = %v (%v)", description, err)
	}

	// Formats Go cannot decode fall back to the preview Nextcloud renders.
	h.fails("nc_read_image", args("target", "/Pics/photo.heic"), "could not produce a preview")
	h.fake.SetPreview(heic, pngImage(t, 64, 64))
	description, content = h.binaryCall("nc_read_image", args("target", "/Pics/photo.heic"))
	if description["source"] != "nextcloud preview" || content[0].(*mcp.ImageContent).MIMEType != "image/png" {
		t.Errorf("preview = %v", description)
	}
	h.fails("nc_read_image", args("target", "/Pics/notes.txt"), "not an image")
	h.fails("nc_read_image", args("target", "/Pics/missing.png"), "Not found")
}

func TestRenderPDF(t *testing.T) {
	if pdfrender.Find() == "" {
		t.Skip("pdftoppm is not installed")
	}
	h := newHarness(t, nil)
	h.fake.AddFile("/doc.pdf", samples.PDF("Page one", "Page two", "Page three"))
	h.fake.AddFile("/notes.md", []byte("x"))

	description, content := h.binaryCall("nc_render_pdf", args("target", "/doc.pdf", "pages", "1,3", "dpi", 50))
	pages := description["pages"].([]any)
	if len(content) != 2 || len(pages) != 2 || description["total_pages"].(float64) != 3 || pages[1].(map[string]any)["page"].(float64) != 3 {
		t.Fatalf("description = %v", description)
	}
	picture := content[0].(*mcp.ImageContent)
	config, err := png.DecodeConfig(bytes.NewReader(picture.Data))
	if err != nil || picture.MIMEType != "image/png" || config.Width != 425 || config.Height != 550 {
		t.Errorf("page image = %+v %v", config, err)
	}
	_, content = h.binaryCall("nc_render_pdf", args("target", "/doc.pdf", "max_side", 100))
	if config, _ := png.DecodeConfig(bytes.NewReader(content[0].(*mcp.ImageContent).Data)); config.Height != 100 {
		t.Errorf("max_side not applied: %+v", config)
	}

	h.fails("nc_render_pdf", args("target", "/doc.pdf", "pages", "4"), "page 4 does not exist")
	h.fails("nc_render_pdf", args("target", "/doc.pdf", "pages", "1-9"), "at most 5 pages")
	h.fails("nc_render_pdf", args("target", "/doc.pdf", "pages", "x; rm -rf /"), "not a page selection")
	h.fails("nc_render_pdf", args("target", "/notes.md"), "is not a PDF")
}
