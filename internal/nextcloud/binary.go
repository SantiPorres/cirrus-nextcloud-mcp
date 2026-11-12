package nextcloud

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Chunk is a piece of a file.
type Chunk struct {
	Data        []byte
	Offset      int64
	Total       int64 // size of the whole file; -1 when unknown
	ETag        string
	ContentType string
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)

// DownloadRange fetches length bytes of a file starting at offset. When
// ifMatch is set, the request fails if the file no longer has that ETag, so
// that the pieces of one download all come from the same content.
func (c *Client) DownloadRange(ctx context.Context, target Target, offset, length int64, ifMatch string) (Chunk, error) {
	headers := map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	resp, err := c.do(ctx, http.MethodGet, c.filesURL(target.Abs), nil, headers)
	if err != nil {
		return Chunk{}, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusPreconditionFailed:
		return Chunk{}, Errorf("%s changed while it was being downloaded. Start again from offset 0.", target.Rel)
	case http.StatusRequestedRangeNotSatisfiable:
		return Chunk{}, Errorf("offset %d is past the end of %s.", offset, target.Rel)
	}
	if err := statusError(resp); err != nil {
		return Chunk{}, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil {
		return Chunk{}, Errorf("The download from Nextcloud was interrupted.")
	}
	chunk := Chunk{Offset: offset, Total: -1, ETag: responseETag(resp), ContentType: resp.Header.Get("Content-Type")}
	if resp.StatusCode == http.StatusPartialContent {
		if m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range")); m != nil {
			chunk.Offset, _ = strconv.ParseInt(m[1], 10, 64)
			chunk.Total, _ = strconv.ParseInt(m[3], 10, 64)
		}
	} else {
		// The server ignored the range and sent the file from the start.
		if int64(len(data)) > length || offset > 0 {
			return Chunk{}, Errorf("Nextcloud did not honour the requested range for %s.", target.Rel)
		}
		chunk.Total = int64(len(data))
	}
	if int64(len(data)) > length {
		data = data[:length]
	}
	chunk.Data = data
	return chunk, nil
}

// Preview asks Nextcloud to render a preview of a file, at most side pixels
// on its longest edge. It works for formats Nextcloud can thumbnail.
func (c *Client) Preview(ctx context.Context, fileID string, side int, limit int64) ([]byte, string, error) {
	query := url.Values{"fileId": {fileID}, "x": {strconv.Itoa(side)}, "y": {strconv.Itoa(side)}, "a": {"1"}}
	resp, err := c.do(ctx, http.MethodGet, c.base+"/index.php/core/preview?"+query.Encode(), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", Errorf("Nextcloud cannot produce a preview of this file.")
	}
	if err := statusError(resp); err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, "", Errorf("The preview from Nextcloud could not be read.")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// --- chunked upload -----------------------------------------------------------

var uploadID = regexp.MustCompile(`^cirrus-[0-9a-f]{32}$`)

// MaxUploadChunks is the highest chunk number Nextcloud accepts.
const MaxUploadChunks = 10000

func (c *Client) uploadURL(id string, segments ...string) string {
	return c.davURL("uploads", append([]string{id}, segments...)...)
}

func checkUploadID(id string) error {
	if !uploadID.MatchString(id) {
		return Errorf("upload_id is not valid. Start again with nc_upload_start.")
	}
	return nil
}

// UploadStart opens an upload session for a new file at destination.
func (c *Client) UploadStart(ctx context.Context, id string, destination Target) error {
	if err := checkUploadID(id); err != nil {
		return err
	}
	resp, err := c.do(ctx, "MKCOL", c.uploadURL(id), nil, map[string]string{"Destination": c.filesURL(destination.Abs)})
	if err != nil {
		return err
	}
	defer drain(resp)
	return statusError(resp)
}

// UploadChunk stores piece number index (1-based) of an upload.
func (c *Client) UploadChunk(ctx context.Context, id string, index int, destination Target, data []byte) error {
	if err := checkUploadID(id); err != nil {
		return err
	}
	if index < 1 || index > MaxUploadChunks {
		return Errorf("index must be between 1 and %d.", MaxUploadChunks)
	}
	resp, err := c.do(ctx, http.MethodPut, c.uploadURL(id, fmt.Sprintf("%05d", index)), data, map[string]string{
		"Destination":  c.filesURL(destination.Abs),
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return Errorf("There is no upload %s. It may have expired; start again with nc_upload_start.", id)
	}
	return statusError(resp)
}

// UploadedChunk is one stored piece of an upload session.
type UploadedChunk struct {
	Index int
	Size  int64
}

// UploadChunks lists the pieces stored so far, in order.
func (c *Client) UploadChunks(ctx context.Context, id string) ([]UploadedChunk, error) {
	if err := checkUploadID(id); err != nil {
		return nil, err
	}
	data, err := c.propfindRaw(ctx, c.uploadURL(id), "1", `<d:getcontentlength/><d:resourcetype/>`)
	if err != nil {
		if IsNotFound(err) {
			return nil, Errorf("There is no upload %s. It may have expired; start again with nc_upload_start.", id)
		}
		return nil, err
	}
	var parsed multistatus
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var chunks []UploadedChunk
	for _, response := range parsed.Responses {
		href := response.Href
		if u, err := url.Parse(href); err == nil {
			href = u.Path
		}
		index, err := strconv.Atoi(lastSegment(href))
		if err != nil || strings.HasSuffix(href, "/") {
			continue // the session folder itself, or ".file"
		}
		chunk := UploadedChunk{Index: index}
		for _, propstat := range response.Propstats {
			if strings.Contains(propstat.Status, " 200") {
				chunk.Size, _ = strconv.ParseInt(propstat.Prop.Length, 10, 64)
			}
		}
		chunks = append(chunks, chunk)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return chunks, nil
}

// UploadFinish assembles the pieces into the destination file. It never
// replaces an existing file.
func (c *Client) UploadFinish(ctx context.Context, id string, destination Target, total int64) error {
	if err := checkUploadID(id); err != nil {
		return err
	}
	resp, err := c.do(ctx, "MOVE", c.uploadURL(id, ".file"), nil, map[string]string{
		"Destination": c.filesURL(destination.Abs),
		// "Overwrite: F" is what protects an existing destination. If-None-Match
		// must not be sent: on a MOVE it applies to the source, which always exists.
		"Overwrite":       "F",
		"OC-Total-Length": strconv.FormatInt(total, 10),
	})
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusPreconditionFailed:
		return Errorf("%s already exists. Nothing was written; uploads never replace a file.", destination.Rel)
	case http.StatusConflict, http.StatusNotFound:
		return Errorf("The parent folder of %s does not exist, or the upload expired.", destination.Rel)
	case http.StatusForbidden:
		return Errorf("Nextcloud denied writing %s.", destination.Rel)
	}
	return statusError(resp)
}
