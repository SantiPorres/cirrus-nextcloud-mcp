//go:build integration

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	_ "image/jpeg" // decoder for the scaled result
	"image/png"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/pdfrender"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/samples"
)

func (h *harness) contents(name string, arguments map[string]any) []mcp.Content {
	h.t.Helper()
	result := h.raw(name, arguments)
	if result.IsError {
		h.t.Fatalf("%s failed: %s", name, text(result))
	}
	return result.Content[1:]
}

func TestUploadThenDownloadInParts(t *testing.T) {
	h := newHarness(t, map[string]string{
		"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_MAX_CHUNK_BYTES": "1000", "NEXTCLOUD_MAX_EMBED_BYTES": "1500",
	})
	payload := make([]byte, 2500)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	sum := sha256.Sum256(payload)

	id := h.call("nc_upload_start", args("path", "/data.bin", "total_size", len(payload)))["upload_id"]
	for index, start := 1, 0; start < len(payload); index, start = index+1, start+1000 {
		end := min(start+1000, len(payload))
		h.call("nc_upload_chunk", args("upload_id", id, "path", "/data.bin", "index", index,
			"data_base64", base64.StdEncoding.EncodeToString(payload[start:end])))
	}
	finished := h.call("nc_upload_finish", args("upload_id", id, "path", "/data.bin", "sha256", hex.EncodeToString(sum[:])))
	if finished["verified"] != true || finished["sha256_matches"] != true || finished["size_bytes"].(float64) != 2500 {
		t.Fatalf("finished = %v", finished)
	}
	if !bytes.Equal([]byte(h.get("/data.bin")), payload) {
		t.Fatal("the stored file differs from what was uploaded")
	}

	// A second upload to the same path is refused at start; and Nextcloud itself refuses the final step.
	h.fails("nc_upload_start", args("path", "/data.bin"), "already exists")
	second := h.call("nc_upload_start", args("path", "/other.bin"))["upload_id"]
	h.call("nc_upload_chunk", args("upload_id", second, "path", "/other.bin", "index", 1, "data_base64", base64.StdEncoding.EncodeToString([]byte("x"))))
	h.put("/other.bin", "appeared meanwhile")
	h.fails("nc_upload_finish", args("upload_id", second, "path", "/other.bin"), "already exists")
	if h.get("/other.bin") != "appeared meanwhile" {
		t.Error("an upload replaced an existing file")
	}

	h.fails("nc_download", args("target", "/data.bin"), "too large to return in one piece")
	var assembled []byte
	arguments := args("target", "/data.bin", "offset", 0, "length", 1000)
	for range 5 {
		description := h.call("nc_download", arguments)
		blob := h.contents("nc_download", arguments)[0].(*mcp.EmbeddedResource).Resource.Blob
		assembled = append(assembled, blob...)
		next, more := description["next_offset"]
		if !more {
			break
		}
		arguments = args("target", "/data.bin", "offset", next, "length", 1000, "etag", description["etag"])
	}
	if !bytes.Equal(assembled, payload) {
		t.Errorf("downloaded %d bytes that differ from the upload", len(assembled))
	}
}

func TestImagesAndPDFPages(t *testing.T) {
	h := newHarness(t, nil)
	img := image.NewNRGBA(image.Rect(0, 0, 500, 250))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.SetNRGBA(1, 1, color.NRGBA{B: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	h.put("/picture.png", buffer.String())
	h.put("/vector.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`)
	h.put("/doc.pdf", string(samples.PDF("Integration page")))

	picture := h.contents("nc_read_image", args("target", "/picture.png", "max_side", 200))[0].(*mcp.ImageContent)
	if config, _, err := image.DecodeConfig(bytes.NewReader(picture.Data)); err != nil || config.Width != 200 || config.Height != 100 {
		t.Errorf("scaled image = %+v %v", config, err)
	}
	// SVG cannot be decoded here and stock Nextcloud does not preview it: a clear error, not a crash.
	if result := h.raw("nc_read_image", args("target", "/vector.svg")); !result.IsError {
		t.Log("this Nextcloud rendered a preview of the SVG")
	}

	if pdfrender.Find() == "" {
		t.Log("pdftoppm is not installed: nc_render_pdf not exercised")
		return
	}
	page := h.contents("nc_render_pdf", args("target", "/doc.pdf", "dpi", 50))[0].(*mcp.ImageContent)
	if config, err := png.DecodeConfig(bytes.NewReader(page.Data)); err != nil || config.Width != 425 {
		t.Errorf("page = %+v %v", config, err)
	}
}
