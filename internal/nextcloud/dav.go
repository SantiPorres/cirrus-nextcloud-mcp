package nextcloud

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Entry describes a file or folder inside the configured root.
type Entry struct {
	Name     string
	Path     string // relative to the root
	IsFolder bool
	// Size in bytes; for folders, the size of their content. -1 when unknown.
	Size        int64
	MIMEType    string
	Modified    string
	ETag        string
	FileID      string
	Permissions string
	// Created and Uploaded are Unix timestamps; 0 when Nextcloud does not know them.
	Created  int64
	Uploaded int64
	Favorite bool

	abs string
}

// Target returns the entry as a Target for further operations.
func (e Entry) Target() Target { return Target{Abs: e.abs, Rel: e.Path, Name: e.Name} }

const davProps = `<d:resourcetype/><d:getcontentlength/><d:getcontenttype/><d:getlastmodified/><d:getetag/>` +
	`<oc:fileid/><oc:permissions/><oc:size/><oc:favorite/><nc:creation_time/><nc:upload_time/>`

const davNamespaces = `xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"`

type multistatus struct {
	Responses []struct {
		Href      string `xml:"DAV: href"`
		Propstats []struct {
			Status string `xml:"DAV: status"`
			Prop   struct {
				ResourceType struct {
					Collection *struct{} `xml:"DAV: collection"`
				} `xml:"DAV: resourcetype"`
				Length       string `xml:"DAV: getcontentlength"`
				ContentType  string `xml:"DAV: getcontenttype"`
				LastModified string `xml:"DAV: getlastmodified"`
				ETag         string `xml:"DAV: getetag"`
				FileID       string `xml:"http://owncloud.org/ns fileid"`
				Permissions  string `xml:"http://owncloud.org/ns permissions"`
				Size         string `xml:"http://owncloud.org/ns size"`
				Favorite     string `xml:"http://owncloud.org/ns favorite"`
				CreationTime string `xml:"http://nextcloud.org/ns creation_time"`
				UploadTime   string `xml:"http://nextcloud.org/ns upload_time"`

				TrashName     string `xml:"http://nextcloud.org/ns trashbin-filename"`
				TrashLocation string `xml:"http://nextcloud.org/ns trashbin-original-location"`
				TrashTime     string `xml:"http://nextcloud.org/ns trashbin-deletion-time"`
				VersionLabel  string `xml:"http://nextcloud.org/ns version-label"`
				VersionAuthor string `xml:"http://nextcloud.org/ns version-author"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// parseEntries reads a multistatus body and drops everything outside the root.
func (c *Client) parseEntries(body []byte) ([]Entry, error) {
	var parsed multistatus
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var entries []Entry
	for _, response := range parsed.Responses {
		href := response.Href
		if u, err := url.Parse(href); err == nil {
			href = u.Path // unescaped, without scheme and host
		}
		if !strings.HasPrefix(href, c.filesPrefix) {
			continue
		}
		abs := strings.TrimRight(href[len(c.filesPrefix):], "/")
		if abs != "" && !strings.HasPrefix(abs, "/") {
			continue // a different user whose name merely starts the same
		}
		rel, inside := c.Rel(abs)
		if !inside {
			continue
		}
		entry := Entry{Name: lastSegment(rel), Path: rel, Size: -1, abs: abs}
		for _, propstat := range response.Propstats {
			if !strings.Contains(propstat.Status, " 200") {
				continue
			}
			p := propstat.Prop
			entry.IsFolder = p.ResourceType.Collection != nil
			entry.MIMEType = p.ContentType
			entry.Modified = p.LastModified
			entry.ETag = p.ETag
			entry.FileID = p.FileID
			entry.Permissions = p.Permissions
			entry.Favorite = p.Favorite == "1"
			entry.Created, _ = strconv.ParseInt(p.CreationTime, 10, 64)
			entry.Uploaded, _ = strconv.ParseInt(p.UploadTime, 10, 64)
			for _, raw := range []string{p.Length, p.Size} {
				if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
					entry.Size = n
					break
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// propfindRaw runs a PROPFIND for the given properties and returns the raw multistatus body.
func (c *Client) propfindRaw(ctx context.Context, rawURL, depth, props string) ([]byte, error) {
	body := `<?xml version="1.0" encoding="UTF-8"?><d:propfind ` + davNamespaces + `><d:prop>` + props + `</d:prop></d:propfind>`
	resp, err := c.do(ctx, "PROPFIND", rawURL, []byte(body), map[string]string{
		"Depth": depth, "Content-Type": "text/xml",
	})
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if err := statusError(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, Errorf("Could not read the answer from Nextcloud.")
	}
	return data, nil
}

func (c *Client) propfind(ctx context.Context, abs, depth string) ([]Entry, error) {
	data, err := c.propfindRaw(ctx, c.filesURL(abs), depth, davProps)
	if err != nil {
		return nil, err
	}
	return c.parseEntries(data)
}

// Stat returns the properties of one file or folder.
func (c *Client) Stat(ctx context.Context, target Target) (Entry, error) {
	entries, err := c.propfind(ctx, target.Abs, "0")
	if err != nil {
		return Entry{}, err
	}
	if len(entries) == 0 {
		return Entry{}, Errorf("Not found, or the Nextcloud user has no access to it.")
	}
	return entries[0], nil
}

// List returns the direct children of a folder, folders first.
func (c *Client) List(ctx context.Context, folder Target) ([]Entry, error) {
	entries, err := c.propfind(ctx, folder.Abs, "1")
	if err != nil {
		return nil, err
	}
	children := entries[:0]
	for _, entry := range entries {
		if entry.abs != strings.TrimRight(folder.Abs, "/") {
			children = append(children, entry)
		}
	}
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].IsFolder != children[j].IsFolder {
			return children[i].IsFolder
		}
		return strings.ToLower(children[i].Name) < strings.ToLower(children[j].Name)
	})
	return children, nil
}

func xmlText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// search runs a WebDAV SEARCH limited to the configured root.
func (c *Client) search(ctx context.Context, where string, limit int) ([]Entry, error) {
	scope := xmlText("/files/" + c.cfg.User + c.cfg.Root)
	body := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:searchrequest ` + davNamespaces + `><d:basicsearch>` +
		`<d:select><d:prop>` + davProps + `</d:prop></d:select>` +
		`<d:from><d:scope><d:href>` + scope + `</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
		`<d:where>` + where + `</d:where>` +
		`<d:limit><d:nresults>` + strconv.Itoa(limit) + `</d:nresults></d:limit>` +
		`</d:basicsearch></d:searchrequest>`
	resp, err := c.do(ctx, "SEARCH", c.base+"/remote.php/dav/", []byte(body), map[string]string{"Content-Type": "text/xml"})
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if err := statusError(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, Errorf("Could not read the answer from Nextcloud.")
	}
	return c.parseEntries(data)
}

// SearchByName finds files and folders whose name contains query (case-insensitive).
func (c *Client) SearchByName(ctx context.Context, query string, limit int) ([]Entry, error) {
	literal := strings.TrimSpace(query)
	where := `<d:like><d:prop><d:displayname/></d:prop><d:literal>%` + xmlText(literal) + `%</d:literal></d:like>`
	return c.search(ctx, where, limit)
}

// FindByID finds a file or folder from its numeric id (the number in /f/<id> links).
func (c *Client) FindByID(ctx context.Context, fileID string) (Entry, error) {
	where := `<d:eq><d:prop><oc:fileid/></d:prop><d:literal>` + xmlText(fileID) + `</d:literal></d:eq>`
	entries, err := c.search(ctx, where, 5)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.FileID == fileID {
			return entry, nil
		}
	}
	hint := "."
	if c.cfg.Root != "" {
		hint = " and located inside the folder this server is restricted to."
	}
	return Entry{}, Errorf("File #%s was not found. Make sure it is shared with the Nextcloud account%s", fileID, hint)
}

// Resolve turns a path (relative to the root) or an internal /f/<id> link into a Target.
// Links cost one request; paths none.
func (c *Client) Resolve(ctx context.Context, target string, needFile bool) (Target, error) {
	target = strings.TrimSpace(target)
	if !IsLink(target) {
		return c.PathTarget(target, needFile)
	}
	fileID, err := c.ParseFileLink(target)
	if err != nil {
		return Target{}, err
	}
	entry, err := c.FindByID(ctx, fileID)
	if err != nil {
		return Target{}, err
	}
	if needFile && entry.IsFolder {
		return Target{}, Errorf("The link points to a folder, not to a file.")
	}
	return entry.Target(), nil
}

// Download fetches a whole file, refusing anything above limit bytes. It
// returns the content and the ETag to use in a later conditional write.
func (c *Client) Download(ctx context.Context, target Target, limit int64) ([]byte, string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.filesURL(target.Abs), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer drain(resp)
	if err := statusError(resp); err != nil {
		return nil, "", err
	}
	if resp.ContentLength > limit {
		return nil, "", Errorf("The file is too large (%d bytes, limit %d).", resp.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", Errorf("The download from Nextcloud was interrupted.")
	}
	if int64(len(data)) > limit {
		return nil, "", Errorf("The file is too large (limit %d bytes).", limit)
	}
	return data, responseETag(resp), nil
}

func responseETag(resp *http.Response) string {
	if etag := resp.Header.Get("ETag"); etag != "" {
		return etag
	}
	return resp.Header.Get("OC-ETag")
}

// PutCondition says when a PUT may happen. Exactly one of the fields must be
// set: there is no way to overwrite a file unconditionally.
type PutCondition struct {
	// IfMatch is the ETag the existing file must still have.
	IfMatch string
	// CreateOnly makes the write fail when the file already exists.
	CreateOnly bool
}

// Put writes a file under the given condition and returns its new ETag.
func (c *Client) Put(ctx context.Context, target Target, data []byte, contentType string, cond PutCondition) (string, error) {
	headers := map[string]string{"Content-Type": contentType}
	switch {
	case cond.CreateOnly && cond.IfMatch == "":
		headers["If-None-Match"] = "*"
	case !cond.CreateOnly && cond.IfMatch != "":
		headers["If-Match"] = cond.IfMatch
	default:
		return "", Errorf("Refusing to write: Nextcloud did not provide an ETag for this file, so it cannot be replaced safely.")
	}
	resp, err := c.do(ctx, http.MethodPut, c.filesURL(target.Abs), data, headers)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusPreconditionFailed:
		if cond.CreateOnly {
			return "", Errorf("%s already exists. Nothing was written.", target.Rel)
		}
		return "", Errorf("%s was modified by someone else in the meantime. Nothing was written; read it again and retry.", target.Rel)
	case http.StatusConflict, http.StatusNotFound:
		// Nextcloud answers 404, other WebDAV servers 409.
		return "", Errorf("The parent folder of %s does not exist.", target.Rel)
	case http.StatusForbidden:
		return "", Errorf("Nextcloud denied writing %s. The account may only have read access to it.", target.Rel)
	}
	if err := statusError(resp); err != nil {
		return "", err
	}
	return responseETag(resp), nil
}

// Mkcol creates one folder. It reports false when the folder already existed.
func (c *Client) Mkcol(ctx context.Context, target Target) (bool, error) {
	resp, err := c.do(ctx, "MKCOL", c.filesURL(target.Abs), nil, nil)
	if err != nil {
		return false, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusMethodNotAllowed:
		entry, err := c.Stat(ctx, target)
		if err != nil {
			return false, err
		}
		if !entry.IsFolder {
			return false, Errorf("%s already exists and is a file, not a folder.", target.Rel)
		}
		return false, nil
	case http.StatusConflict:
		return false, Errorf("The parent of %s does not exist.", target.Rel)
	case http.StatusForbidden:
		return false, Errorf("Nextcloud denied creating folders here.")
	}
	if err := statusError(resp); err != nil {
		return false, err
	}
	return true, nil
}
