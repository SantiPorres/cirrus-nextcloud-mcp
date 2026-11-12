package nextcloud

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Move renames or moves a file or folder. It never replaces the destination.
func (c *Client) Move(ctx context.Context, source, destination Target) error {
	return c.transfer(ctx, "MOVE", source, destination)
}

// Copy copies a file or folder. It never replaces the destination.
func (c *Client) Copy(ctx context.Context, source, destination Target) error {
	return c.transfer(ctx, "COPY", source, destination)
}

func (c *Client) transfer(ctx context.Context, method string, source, destination Target) error {
	if source.Rel == "/" || destination.Rel == "/" {
		return Errorf("The top folder cannot be moved, copied or replaced.")
	}
	if source.Abs == destination.Abs {
		return Errorf("The source and the destination are the same.")
	}
	if strings.HasPrefix(destination.Abs, source.Abs+"/") {
		return Errorf("A folder cannot be moved or copied into itself.")
	}
	resp, err := c.do(ctx, method, c.filesURL(source.Abs), nil, map[string]string{
		"Destination": c.filesURL(destination.Abs),
		"Overwrite":   "F",
	})
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusPreconditionFailed:
		return Errorf("%s already exists. Nothing was changed; choose another destination.", destination.Rel)
	case http.StatusConflict:
		return Errorf("The parent folder of %s does not exist.", destination.Rel)
	case http.StatusNotFound:
		return &Error{Message: source.Rel + " was not found.", Status: http.StatusNotFound}
	case http.StatusForbidden:
		return Errorf("Nextcloud denied this operation. The account may lack permission on the source or the destination.")
	}
	return statusError(resp)
}

// Delete removes a file or folder. With the trash bin enabled (check
// Capabilities first) Nextcloud moves it to the trash instead of destroying it.
func (c *Client) Delete(ctx context.Context, target Target) error {
	if target.Rel == "/" {
		return Errorf("The top folder cannot be deleted.")
	}
	resp, err := c.do(ctx, http.MethodDelete, c.filesURL(target.Abs), nil, nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusForbidden {
		return Errorf("Nextcloud denied deleting %s.", target.Rel)
	}
	return statusError(resp)
}

// TrashItem is a deleted file or folder that can still be restored.
type TrashItem struct {
	// ID identifies the item in the trash, e.g. "report.md.d1700000000".
	ID string
	// Name is the original file name.
	Name string
	// OriginalPath is where it was, relative to the root.
	OriginalPath string
	// DeletedAt is a Unix timestamp.
	DeletedAt int64
	Size      int64
	IsFolder  bool
	FileID    string
}

const trashProps = `<nc:trashbin-filename/><nc:trashbin-original-location/><nc:trashbin-deletion-time/>` +
	`<d:getcontentlength/><d:resourcetype/><oc:fileid/><oc:size/>`

// TrashList returns the deleted items whose original location is inside the root, newest first.
func (c *Client) TrashList(ctx context.Context) ([]TrashItem, error) {
	data, err := c.propfindRaw(ctx, c.davURL("trashbin", "trash"), "1", trashProps)
	if err != nil {
		return nil, err
	}
	var parsed multistatus
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var items []TrashItem
	for _, response := range parsed.Responses {
		href := response.Href
		if u, err := url.Parse(href); err == nil {
			href = u.Path
		}
		item := TrashItem{ID: lastSegment(href), Size: -1}
		found := false
		for _, propstat := range response.Propstats {
			if !strings.Contains(propstat.Status, " 200") || propstat.Prop.TrashLocation == "" {
				continue
			}
			p := propstat.Prop
			rel, inside := c.Rel("/" + strings.Trim(p.TrashLocation, "/"))
			if !inside || rel == "/" {
				continue
			}
			found = true
			item.Name, item.OriginalPath, item.FileID = p.TrashName, rel, p.FileID
			item.IsFolder = p.ResourceType.Collection != nil
			item.DeletedAt, _ = strconv.ParseInt(p.TrashTime, 10, 64)
			for _, raw := range []string{p.Length, p.Size} {
				if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
					item.Size = n
					break
				}
			}
		}
		if found {
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].DeletedAt > items[j].DeletedAt })
	return items, nil
}

// TrashRestore puts a deleted item back where it was. Only items listed by
// TrashList (that is, originally inside the root) can be restored.
func (c *Client) TrashRestore(ctx context.Context, id string) (TrashItem, error) {
	items, err := c.TrashList(ctx)
	if err != nil {
		return TrashItem{}, err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		resp, err := c.do(ctx, "MOVE", c.davURL("trashbin", "trash", id), nil, map[string]string{
			"Destination": c.davURL("trashbin", "restore", id),
		})
		if err != nil {
			return TrashItem{}, err
		}
		defer drain(resp)
		return item, statusError(resp)
	}
	return TrashItem{}, Errorf("There is no item %q in the trash. Call nc_trash_list for the current ids.", id)
}

// Version is one stored version of a file.
type Version struct {
	// ID is the version identifier (a Unix timestamp).
	ID       string
	Modified string
	Size     int64
	Label    string
	Author   string
}

var versionID = regexp.MustCompile(`^\d+$`)

const versionProps = `<d:getlastmodified/><d:getcontentlength/><nc:version-label/><nc:version-author/>`

// Versions lists the stored versions of a file, newest first.
func (c *Client) Versions(ctx context.Context, fileID string) ([]Version, error) {
	if !versionID.MatchString(fileID) {
		return nil, Errorf("This file has no id, so its versions cannot be listed.")
	}
	data, err := c.propfindRaw(ctx, c.davURL("versions", "versions", fileID), "1", versionProps)
	if err != nil {
		return nil, err
	}
	var parsed multistatus
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var versions []Version
	for _, response := range parsed.Responses {
		href := response.Href
		if u, err := url.Parse(href); err == nil {
			href = u.Path
		}
		id := lastSegment(href)
		if id == fileID || !versionID.MatchString(id) {
			continue // the collection itself
		}
		version := Version{ID: id, Size: -1}
		for _, propstat := range response.Propstats {
			if !strings.Contains(propstat.Status, " 200") {
				continue
			}
			p := propstat.Prop
			version.Modified, version.Label, version.Author = p.LastModified, p.VersionLabel, p.VersionAuthor
			if n, err := strconv.ParseInt(p.Length, 10, 64); err == nil {
				version.Size = n
			}
		}
		versions = append(versions, version)
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].ID > versions[j].ID })
	return versions, nil
}

// RestoreVersion makes a stored version the current content of the file.
// Nextcloud keeps the replaced content as another version.
func (c *Client) RestoreVersion(ctx context.Context, fileID, id string) error {
	if !versionID.MatchString(fileID) || !versionID.MatchString(id) {
		return Errorf("%q is not a valid version id. Call nc_versions_list for the current ids.", id)
	}
	resp, err := c.do(ctx, "MOVE", c.davURL("versions", "versions", fileID, id), nil, map[string]string{
		"Destination": c.davURL("versions", "restore", "target"),
	})
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return Errorf("Version %s does not exist. Call nc_versions_list for the current ids.", id)
	}
	return statusError(resp)
}
