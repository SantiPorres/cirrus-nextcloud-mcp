// Package binary implements the "binary" tool group: downloading and
// uploading arbitrary files, and returning images and PDF pages as image
// content. Binary data travels as MCP resource or image content, never as
// base64 inside the text the model reads.
package binary

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/imaging"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/pdfrender"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const (
	defaultMaxSide = 1568
	minMaxSide     = 64
	maxMaxSide     = 4096
	defaultDPI     = 100
	minDPI         = 50
	maxDPI         = 200
	maxPDFPages    = 5
	previewLimit   = 20 << 20
)

type group struct {
	client   *nextcloud.Client
	cfg      *config.Config
	pdftoppm string
}

// Register adds the binary tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client, cfg: r.Config, pdftoppm: pdfrender.Find()}

	tools.Add(r, tools.Spec{
		Name: "nc_download", Title: "Download file", Group: config.GroupBinary,
		Description: "Downloads a file of any type as an embedded resource (not as text). Small files come in one " +
			"piece. For larger files pass offset and length to fetch a part: the result has \"next_offset\" for the " +
			"following call, and \"etag\", which should be passed back so that every part comes from the same " +
			"version of the file. To read documents as text use nc_read_file instead.",
	}, g.download)

	tools.Add(r, tools.Spec{
		Name: "nc_upload_start", Title: "Start upload", Group: config.GroupBinary, Permission: tools.Write,
		Description: "Starts the upload of a new file of any type and returns an \"upload_id\". Then send the " +
			"content with nc_upload_chunk and create the file with nc_upload_finish. Uploads only create: if " +
			"the path is taken the upload is refused, here and again when finishing. The parent folder must exist.",
	}, g.uploadStart)

	tools.Add(r, tools.Spec{
		Name: "nc_upload_chunk", Title: "Upload chunk", Group: config.GroupBinary, Permission: tools.Write, Idempotent: true,
		Description: "Sends one piece of an upload, base64-encoded. Pieces are numbered from 1 and joined in that " +
			"order; sending the same index again replaces that piece.",
	}, g.uploadChunk)

	tools.Add(r, tools.Spec{
		Name: "nc_upload_finish", Title: "Finish upload", Group: config.GroupBinary, Permission: tools.Write,
		Description: "Joins the pieces of an upload into the new file. It fails, writing nothing, if a file " +
			"appeared at that path in the meantime or if a piece is missing. Pass \"sha256\" to have the stored " +
			"file checked against it.",
	}, g.uploadFinish)

	tools.Add(r, tools.Spec{
		Name: "nc_read_image", Title: "Read image", Group: config.GroupBinary,
		Description: "Returns an image file as image content the model can look at. Large pictures are scaled " +
			"down to max_side and photos are turned upright. Supports JPEG, PNG, GIF, WebP, BMP and TIFF; for " +
			"other formats (such as HEIC or SVG) it falls back to the preview Nextcloud can generate.",
	}, g.readImage)

	renderSpec := tools.Spec{
		Name: "nc_render_pdf", Title: "Render PDF pages", Group: config.GroupBinary,
		Description: "Renders pages of a PDF as images, for scanned documents, diagrams or layouts that " +
			"nc_read_file cannot convey as text. At most 5 pages per call.",
	}
	if g.pdftoppm == "" {
		renderSpec.Unavailable = "pdftoppm (poppler-utils) is not installed on the server"
	}
	tools.Add(r, renderSpec, g.renderPDF)
}

// result builds a tool result made of a JSON description followed by binary content.
func result(description map[string]any, content ...mcp.Content) *mcp.CallToolResult {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(description)
	all := append([]mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(buffer.String())}}, content...)
	return &mcp.CallToolResult{Content: all}
}

func resourceURI(target nextcloud.Target) string {
	parts := strings.Split(strings.Trim(target.Rel, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "nextcloud:///" + strings.Join(parts, "/")
}

func cleanMIME(contentType string) string {
	mime, _, _ := strings.Cut(contentType, ";")
	if mime = strings.TrimSpace(mime); mime != "" {
		return mime
	}
	return "application/octet-stream"
}

// --- download ---------------------------------------------------------------

type downloadInput struct {
	Target string `json:"target" jsonschema:"A path relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	Offset int64  `json:"offset,omitempty" jsonschema:"Byte position to start from. Defaults to 0."`
	Length int64  `json:"length,omitempty" jsonschema:"Number of bytes to fetch. Leave out to download the whole file in one piece (small files only)."`
	ETag   string `json:"etag,omitempty" jsonschema:"The \"etag\" returned by the previous part, so that the download fails if the file changed in between."`
}

func (g *group) download(ctx context.Context, in downloadInput) (any, error) {
	if in.Offset < 0 || in.Length < 0 {
		return nil, nextcloud.Errorf("offset and length must not be negative.")
	}
	target, err := g.client.Resolve(ctx, in.Target, true)
	if err != nil {
		return nil, err
	}
	length := in.Length
	whole := in.Offset == 0 && in.Length == 0
	if whole {
		entry, err := g.client.Stat(ctx, target)
		if err != nil {
			return nil, err
		}
		if entry.IsFolder {
			return nil, nextcloud.Errorf("%s is a folder.", target.Rel)
		}
		if entry.Size > g.cfg.MaxEmbedBytes {
			return nil, nextcloud.Errorf("%s is %d bytes, too large to return in one piece (limit %d). Download it in parts: pass offset=0 and length up to %d, then follow next_offset.",
				target.Rel, entry.Size, g.cfg.MaxEmbedBytes, g.cfg.MaxChunkBytes)
		}
		length = g.cfg.MaxEmbedBytes
	} else {
		if length == 0 {
			length = g.cfg.MaxChunkBytes
		}
		if length > g.cfg.MaxChunkBytes {
			return nil, nextcloud.Errorf("length must be at most %d bytes per call.", g.cfg.MaxChunkBytes)
		}
	}

	chunk, err := g.client.DownloadRange(ctx, target, in.Offset, length, strings.TrimSpace(in.ETag))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(chunk.Data)
	mime := cleanMIME(chunk.ContentType)
	description := map[string]any{
		"path": target.Rel, "filename": target.Name, "mime_type": mime, "offset": chunk.Offset,
		"returned_bytes": len(chunk.Data), "sha256": hex.EncodeToString(sum[:]), "etag": chunk.ETag,
	}
	end := chunk.Offset + int64(len(chunk.Data))
	if chunk.Total >= 0 {
		description["total_bytes"] = chunk.Total
		description["complete"] = chunk.Offset == 0 && end >= chunk.Total
		if end < chunk.Total {
			description["next_offset"] = end
		}
	}
	return result(description, &mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{URI: resourceURI(target), MIMEType: mime, Blob: chunk.Data},
	}), nil
}

// --- upload -----------------------------------------------------------------

type uploadStartInput struct {
	Path      string `json:"path" jsonschema:"Where to create the file, relative to the top folder, e.g. \"/Scans/invoice.pdf\"."`
	TotalSize int64  `json:"total_size,omitempty" jsonschema:"Size of the whole file in bytes, if known. Used to refuse files that are too large before sending them."`
}

func (g *group) destination(ctx context.Context, path string) (nextcloud.Target, error) {
	target, err := g.client.PathTarget(path, true)
	if err != nil {
		return target, err
	}
	switch _, err := g.client.Stat(ctx, target); {
	case err == nil:
		return target, nextcloud.Errorf("%s already exists. Uploads never replace a file; choose another path.", target.Rel)
	case !nextcloud.IsNotFound(err):
		return target, err
	}
	return target, nil
}

func (g *group) uploadStart(ctx context.Context, in uploadStartInput) (any, error) {
	if in.TotalSize < 0 || in.TotalSize > g.cfg.MaxUploadBytes {
		return nil, nextcloud.Errorf("The file is too large to upload (%d bytes, limit %d).", in.TotalSize, g.cfg.MaxUploadBytes)
	}
	target, err := g.destination(ctx, in.Path)
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, nextcloud.Errorf("Could not create an upload id.")
	}
	id := "cirrus-" + hex.EncodeToString(random)
	if err := g.client.UploadStart(ctx, id, target); err != nil {
		return nil, err
	}
	return map[string]any{
		"upload_id": id, "path": target.Rel, "max_chunk_bytes": g.cfg.MaxChunkBytes, "max_total_bytes": g.cfg.MaxUploadBytes,
		"next": "Send the content with nc_upload_chunk (index 1, 2, ...), then call nc_upload_finish with the same path.",
	}, nil
}

type uploadChunkInput struct {
	UploadID   string `json:"upload_id" jsonschema:"The \"upload_id\" returned by nc_upload_start."`
	Path       string `json:"path" jsonschema:"The same path given to nc_upload_start."`
	Index      int    `json:"index" jsonschema:"Number of this piece, starting at 1."`
	DataBase64 string `json:"data_base64" jsonschema:"The bytes of this piece, base64-encoded."`
}

func decodeBase64(text string) ([]byte, error) {
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := encoding.DecodeString(text); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("not base64")
}

func (g *group) uploadChunk(ctx context.Context, in uploadChunkInput) (any, error) {
	target, err := g.client.PathTarget(in.Path, true)
	if err != nil {
		return nil, err
	}
	data, err := decodeBase64(in.DataBase64)
	if err != nil {
		return nil, nextcloud.Errorf("data_base64 is not valid base64.")
	}
	if len(data) == 0 {
		return nil, nextcloud.Errorf("The piece is empty.")
	}
	if int64(len(data)) > g.cfg.MaxChunkBytes {
		return nil, nextcloud.Errorf("The piece is too large (%d bytes, limit %d per piece).", len(data), g.cfg.MaxChunkBytes)
	}
	if err := g.client.UploadChunk(ctx, strings.TrimSpace(in.UploadID), in.Index, target, data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return map[string]any{"upload_id": in.UploadID, "index": in.Index, "received_bytes": len(data), "sha256": hex.EncodeToString(sum[:])}, nil
}

type uploadFinishInput struct {
	UploadID string `json:"upload_id" jsonschema:"The \"upload_id\" returned by nc_upload_start."`
	Path     string `json:"path" jsonschema:"The same path given to nc_upload_start."`
	SHA256   string `json:"sha256,omitempty" jsonschema:"Optional SHA-256 of the whole file (hex). The stored file is checked against it."`
}

func (g *group) uploadFinish(ctx context.Context, in uploadFinishInput) (any, error) {
	id := strings.TrimSpace(in.UploadID)
	target, err := g.destination(ctx, in.Path)
	if err != nil {
		return nil, err
	}
	chunks, err := g.client.UploadChunks(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, nextcloud.Errorf("No piece has been sent for this upload.")
	}
	var total int64
	for position, chunk := range chunks {
		if chunk.Index != position+1 {
			return nil, nextcloud.Errorf("Piece %d is missing. Send it with nc_upload_chunk before finishing.", position+1)
		}
		total += chunk.Size
	}
	if total > g.cfg.MaxUploadBytes {
		return nil, nextcloud.Errorf("The file is too large to upload (%d bytes, limit %d). Nothing was written.", total, g.cfg.MaxUploadBytes)
	}
	if err := g.client.UploadFinish(ctx, id, target, total); err != nil {
		return nil, err
	}

	out := map[string]any{"path": target.Rel, "status": "created", "size_bytes": total, "pieces": len(chunks)}
	entry, err := g.client.Stat(ctx, target)
	if err != nil {
		out["verified"] = false
		return out, nil
	}
	out["etag"], out["url"] = entry.ETag, g.client.FileLink(entry.FileID)
	out["verified"] = entry.Size == total
	if want := strings.ToLower(strings.TrimSpace(in.SHA256)); want != "" {
		if total > g.cfg.MaxDownloadBytes {
			out["sha256_checked"] = false
			out["note"] = "The file is too large to be read back for the SHA-256 check; only its size was verified."
			return out, nil
		}
		data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
		if err != nil {
			out["sha256_checked"] = false
			return out, nil
		}
		sum := sha256.Sum256(data)
		matches := hex.EncodeToString(sum[:]) == want
		out["sha256_checked"], out["sha256_matches"] = true, matches
		if !matches {
			out["verified"] = false
			out["warning"] = "The stored file does not match the given SHA-256. It was left in place; delete or replace it deliberately."
		}
	}
	return out, nil
}

// --- images -----------------------------------------------------------------

func clampSide(side int) int {
	if side <= 0 {
		return defaultMaxSide
	}
	return min(max(side, minMaxSide), maxMaxSide)
}

type imageInput struct {
	Target  string `json:"target" jsonschema:"The image: a path relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	MaxSide int    `json:"max_side,omitempty" jsonschema:"Largest width or height of the returned image, in pixels (64-4096). Defaults to 1568."`
}

func imageDescription(target nextcloud.Target, processed imaging.Result, source string) map[string]any {
	return map[string]any{
		"path": target.Rel, "filename": target.Name, "mime_type": processed.MIMEType,
		"width": processed.Width, "height": processed.Height,
		"original_width": processed.OriginalWidth, "original_height": processed.OriginalHeight,
		"resized": processed.Resized, "size_bytes": len(processed.Data), "source": source,
	}
}

func (g *group) readImage(ctx context.Context, in imageInput) (any, error) {
	side := clampSide(in.MaxSide)
	target, err := g.client.Resolve(ctx, in.Target, true)
	if err != nil {
		return nil, err
	}
	data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return nil, err
	}
	processed, err := imaging.Process(data, imaging.Options{MaxSide: side})
	source := "file"
	switch {
	case errors.Is(err, imaging.ErrTooLarge):
		return nil, nextcloud.Errorf("%s has too many pixels to be processed.", target.Rel)
	case err != nil:
		// Not a format this server decodes: let Nextcloud render a preview.
		entry, statErr := g.client.Stat(ctx, target)
		if statErr != nil {
			return nil, statErr
		}
		preview, _, previewErr := g.client.Preview(ctx, entry.FileID, side, previewLimit)
		if previewErr != nil {
			return nil, nextcloud.Errorf("%s is not an image this server can read, and Nextcloud could not produce a preview of it.", target.Rel)
		}
		if processed, err = imaging.Process(preview, imaging.Options{MaxSide: side}); err != nil {
			return nil, nextcloud.Errorf("%s is not an image this server can read.", target.Rel)
		}
		source = "nextcloud preview"
	}
	return result(imageDescription(target, processed, source),
		&mcp.ImageContent{Data: processed.Data, MIMEType: processed.MIMEType}), nil
}

type renderInput struct {
	Target  string `json:"target" jsonschema:"The PDF: a path relative to the top folder, or an internal file link (https://<host>/f/<id>)."`
	Pages   string `json:"pages,omitempty" jsonschema:"Pages to render: \"1\", \"2-4\" or \"1,3,5\" (at most 5). Defaults to \"1\"."`
	DPI     int    `json:"dpi,omitempty" jsonschema:"Resolution in dots per inch (50-200). Defaults to 100."`
	MaxSide int    `json:"max_side,omitempty" jsonschema:"Largest width or height of each returned image, in pixels (64-4096). Defaults to 1568."`
}

// pageCount returns the number of pages, or 0 when it cannot be determined.
func pageCount(data []byte) (count int) {
	defer func() {
		if recover() != nil {
			count = 0
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}
	return reader.NumPage()
}

func (g *group) renderPDF(ctx context.Context, in renderInput) (any, error) {
	pages, err := pdfrender.ParsePages(in.Pages, maxPDFPages)
	if err != nil {
		return nil, nextcloud.Errorf("pages: %s.", err.Error())
	}
	dpi := in.DPI
	if dpi <= 0 {
		dpi = defaultDPI
	}
	dpi = min(max(dpi, minDPI), maxDPI)
	side := clampSide(in.MaxSide)

	target, err := g.client.Resolve(ctx, in.Target, true)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(target.Name), ".pdf") {
		return nil, nextcloud.Errorf("%s is not a PDF.", target.Name)
	}
	data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return nil, err
	}
	total := pageCount(data)

	var rendered []map[string]any
	var content []mcp.Content
	for _, page := range pages {
		if total > 0 && page > total {
			return nil, nextcloud.Errorf("%s has %d page(s); page %d does not exist.", target.Rel, total, page)
		}
		image, err := pdfrender.Page(ctx, g.pdftoppm, data, page, dpi)
		if errors.Is(err, pdfrender.ErrNoSuchPage) {
			return nil, nextcloud.Errorf("%s does not have a page %d.", target.Rel, page)
		}
		if err != nil {
			return nil, nextcloud.Errorf("Could not render page %d of %s: %s.", page, target.Rel, err.Error())
		}
		processed, err := imaging.Process(image, imaging.Options{MaxSide: side, PreferPNG: true})
		if err != nil {
			return nil, nextcloud.Errorf("Could not process the image of page %d.", page)
		}
		rendered = append(rendered, map[string]any{
			"page": page, "width": processed.Width, "height": processed.Height, "size_bytes": len(processed.Data),
		})
		content = append(content, &mcp.ImageContent{Data: processed.Data, MIMEType: processed.MIMEType})
	}
	description := map[string]any{"path": target.Rel, "filename": target.Name, "dpi": dpi, "pages": rendered}
	if total > 0 {
		description["total_pages"] = total
	}
	return result(description, content...), nil
}
