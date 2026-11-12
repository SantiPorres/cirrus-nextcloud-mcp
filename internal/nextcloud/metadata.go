package nextcloud

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var numericID = regexp.MustCompile(`^\d+$`)

// send runs a WebDAV request that returns no interesting body and maps its status.
func (c *Client) send(ctx context.Context, method, rawURL string, body []byte, headers map[string]string, statuses map[int]string) (*http.Response, error) {
	resp, err := c.do(ctx, method, rawURL, body, headers)
	if err != nil {
		return nil, err
	}
	if message, known := statuses[resp.StatusCode]; known {
		drain(resp)
		return nil, &Error{Message: message, Status: resp.StatusCode}
	}
	if err := statusError(resp); err != nil {
		drain(resp)
		return nil, err
	}
	return resp, nil
}

// report runs a REPORT and returns the multistatus body.
func (c *Client) report(ctx context.Context, rawURL, body string) ([]byte, error) {
	resp, err := c.send(ctx, "REPORT", rawURL, []byte(body), map[string]string{"Content-Type": "text/xml"}, nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, Errorf("Could not read the answer from Nextcloud.")
	}
	return data, nil
}

// --- system tags --------------------------------------------------------------

// Tag is a Nextcloud system tag.
type Tag struct {
	ID         string
	Name       string
	Visible    bool
	Assignable bool
}

const tagProps = `<oc:id/><oc:display-name/><oc:user-visible/><oc:user-assignable/>`

func (c *Client) parseTags(data []byte) ([]Tag, error) {
	var parsed multistatus
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var tags []Tag
	for _, response := range parsed.Responses {
		for _, propstat := range response.Propstats {
			p := propstat.Prop
			if !strings.Contains(propstat.Status, " 200") || p.ID == "" {
				continue
			}
			tags = append(tags, Tag{ID: p.ID, Name: p.DisplayName, Visible: p.UserVisible != "false", Assignable: p.UserAssignable != "false"})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return strings.ToLower(tags[i].Name) < strings.ToLower(tags[j].Name) })
	return tags, nil
}

// Tags lists every system tag the account can see.
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	data, err := c.propfindRaw(ctx, c.base+"/remote.php/dav/systemtags/", "1", tagProps)
	if err != nil {
		return nil, err
	}
	return c.parseTags(data)
}

// FileTags lists the tags assigned to a file or folder.
func (c *Client) FileTags(ctx context.Context, fileID string) ([]Tag, error) {
	if !numericID.MatchString(fileID) {
		return nil, Errorf("This item has no id, so its tags cannot be read.")
	}
	data, err := c.propfindRaw(ctx, c.base+"/remote.php/dav/systemtags-relations/files/"+fileID, "1", tagProps)
	if err != nil {
		return nil, err
	}
	return c.parseTags(data)
}

// FindTag resolves a tag given by name (case-insensitive) or by numeric id.
func (c *Client) FindTag(ctx context.Context, nameOrID string) (Tag, error) {
	wanted := strings.TrimSpace(nameOrID)
	tags, err := c.Tags(ctx)
	if err != nil {
		return Tag{}, err
	}
	for _, tag := range tags {
		if strings.EqualFold(tag.Name, wanted) {
			return tag, nil
		}
	}
	for _, tag := range tags {
		if tag.ID == wanted {
			return tag, nil
		}
	}
	return Tag{}, Errorf("There is no tag %q. Call nc_tags_list for the existing tags, or create it with nc_tag_create.", wanted)
}

// CreateTag creates a visible, assignable system tag.
func (c *Client) CreateTag(ctx context.Context, name string) (Tag, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "userVisible": true, "userAssignable": true})
	resp, err := c.send(ctx, http.MethodPost, c.base+"/remote.php/dav/systemtags/", body,
		map[string]string{"Content-Type": "application/json"},
		map[int]string{
			http.StatusConflict:  "A tag named \"" + name + "\" already exists.",
			http.StatusForbidden: "Nextcloud does not allow this account to create tags.",
		})
	if err != nil {
		return Tag{}, err
	}
	defer drain(resp)
	return Tag{ID: lastSegment(resp.Header.Get("Content-Location")), Name: name, Visible: true, Assignable: true}, nil
}

func (c *Client) tagRelationURL(fileID, tagID string) (string, error) {
	if !numericID.MatchString(fileID) || !numericID.MatchString(tagID) {
		return "", Errorf("The file or the tag has no valid id.")
	}
	return c.base + "/remote.php/dav/systemtags-relations/files/" + fileID + "/" + tagID, nil
}

// AssignTag puts a tag on a file or folder. It reports false when it was already there.
func (c *Client) AssignTag(ctx context.Context, fileID, tagID string) (bool, error) {
	rawURL, err := c.tagRelationURL(fileID, tagID)
	if err != nil {
		return false, err
	}
	resp, err := c.do(ctx, http.MethodPut, rawURL, nil, nil)
	if err != nil {
		return false, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusConflict:
		return false, nil
	case http.StatusForbidden:
		return false, Errorf("Nextcloud does not allow this account to assign that tag.")
	}
	return true, statusError(resp)
}

// RemoveTag takes a tag off a file or folder. It reports false when it was not there.
func (c *Client) RemoveTag(ctx context.Context, fileID, tagID string) (bool, error) {
	rawURL, err := c.tagRelationURL(fileID, tagID)
	if err != nil {
		return false, err
	}
	resp, err := c.do(ctx, http.MethodDelete, rawURL, nil, nil)
	if err != nil {
		return false, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		return false, Errorf("Nextcloud does not allow this account to remove that tag.")
	}
	return true, statusError(resp)
}

// filterFiles runs a filter-files REPORT on the root folder.
func (c *Client) filterFiles(ctx context.Context, rule string) ([]Entry, error) {
	body := `<?xml version="1.0" encoding="UTF-8"?><oc:filter-files ` + davNamespaces + `>` +
		`<d:prop>` + davProps + `</d:prop><oc:filter-rules>` + rule + `</oc:filter-rules></oc:filter-files>`
	data, err := c.report(ctx, c.filesURL(c.cfg.Root), body)
	if err != nil {
		return nil, err
	}
	entries, err := c.parseEntries(data)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path) })
	return entries, nil
}

// FilesWithTag lists the files and folders inside the root that carry a tag.
func (c *Client) FilesWithTag(ctx context.Context, tagID string) ([]Entry, error) {
	if !numericID.MatchString(tagID) {
		return nil, Errorf("The tag has no valid id.")
	}
	return c.filterFiles(ctx, `<oc:systemtag>`+tagID+`</oc:systemtag>`)
}

// --- favorites ----------------------------------------------------------------

// Favorites lists the favorite files and folders inside the root.
func (c *Client) Favorites(ctx context.Context) ([]Entry, error) {
	return c.filterFiles(ctx, `<oc:favorite>1</oc:favorite>`)
}

// SetFavorite marks or unmarks a file or folder as favorite.
func (c *Client) SetFavorite(ctx context.Context, target Target, favorite bool) error {
	value := "0"
	if favorite {
		value = "1"
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><d:propertyupdate ` + davNamespaces + `><d:set><d:prop>` +
		`<oc:favorite>` + value + `</oc:favorite></d:prop></d:set></d:propertyupdate>`
	resp, err := c.send(ctx, "PROPPATCH", c.filesURL(target.Abs), []byte(body), map[string]string{"Content-Type": "text/xml"}, nil)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// --- comments -----------------------------------------------------------------

// Comment is a comment on a file.
type Comment struct {
	ID      string
	Message string
	Author  string
	Name    string
	Created string
}

// Comments lists the comments on a file, newest first.
func (c *Client) Comments(ctx context.Context, fileID string, limit, offset int) ([]Comment, error) {
	if !numericID.MatchString(fileID) {
		return nil, Errorf("This item has no id, so its comments cannot be read.")
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><oc:filter-comments ` + davNamespaces + `>` +
		`<oc:limit>` + strconv.Itoa(limit) + `</oc:limit><oc:offset>` + strconv.Itoa(offset) + `</oc:offset></oc:filter-comments>`
	data, err := c.report(ctx, c.base+"/remote.php/dav/comments/files/"+fileID, body)
	if err != nil {
		return nil, err
	}
	var parsed multistatus
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return nil, Errorf("Nextcloud returned a response that could not be understood.")
	}
	var comments []Comment
	for _, response := range parsed.Responses {
		for _, propstat := range response.Propstats {
			p := propstat.Prop
			if !strings.Contains(propstat.Status, " 200") || p.ID == "" {
				continue
			}
			comments = append(comments, Comment{ID: p.ID, Message: p.Message, Author: p.ActorID, Name: p.ActorDisplayName, Created: p.CreationDateTime})
		}
	}
	return comments, nil
}

// AddComment posts a comment on a file as the account.
func (c *Client) AddComment(ctx context.Context, fileID, message string) error {
	if !numericID.MatchString(fileID) {
		return Errorf("This item has no id, so it cannot be commented.")
	}
	body, _ := json.Marshal(map[string]string{"actorType": "users", "verb": "comment", "message": message})
	resp, err := c.send(ctx, http.MethodPost, c.base+"/remote.php/dav/comments/files/"+fileID, body,
		map[string]string{"Content-Type": "application/json"},
		map[int]string{http.StatusRequestEntityTooLarge: "The comment is too long.", http.StatusBadRequest: "Nextcloud rejected the comment (it may be too long or empty)."})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// --- recent files -------------------------------------------------------------

// RecentFiles lists the files inside the root modified since the given time, newest first.
func (c *Client) RecentFiles(ctx context.Context, since time.Time, limit int) ([]Entry, error) {
	where := `<d:and>` +
		`<d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>` + since.UTC().Format(time.RFC3339) + `</d:literal></d:gt>` +
		`<d:not><d:is-collection/></d:not></d:and>`
	orderBy := `<d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order>`
	return c.searchOrdered(ctx, where, orderBy, limit)
}

// --- user and activity --------------------------------------------------------

// User describes the account the server is connected as.
type User struct {
	ID          string
	DisplayName string
	Email       string
	Groups      []string
	Language    string
	// Quota values are in bytes. Negative totals mean "unlimited" or "unknown".
	QuotaUsed, QuotaFree, QuotaTotal int64
	QuotaRelative                    float64
}

// CurrentUser returns the account's profile and storage quota.
func (c *Client) CurrentUser(ctx context.Context) (User, error) {
	var raw struct {
		ID          string   `json:"id"`
		DisplayName string   `json:"displayname"`
		Email       *string  `json:"email"`
		Groups      []string `json:"groups"`
		Language    string   `json:"language"`
		Quota       struct {
			Free     float64 `json:"free"`
			Used     float64 `json:"used"`
			Total    float64 `json:"total"`
			Relative float64 `json:"relative"`
		} `json:"quota"`
	}
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v2.php/cloud/user", nil, &raw); err != nil {
		return User{}, err
	}
	user := User{
		ID: raw.ID, DisplayName: raw.DisplayName, Groups: raw.Groups, Language: raw.Language,
		QuotaUsed: int64(raw.Quota.Used), QuotaFree: int64(raw.Quota.Free), QuotaTotal: int64(raw.Quota.Total),
		QuotaRelative: raw.Quota.Relative,
	}
	if raw.Email != nil {
		user.Email = *raw.Email
	}
	return user, nil
}

// Activity is one entry of the activity stream.
type Activity struct {
	ID      int64
	Type    string
	User    string
	Subject string
	// Path is the file concerned, relative to the root; "" for non-file activities.
	Path string
	Time string
}

// Activities returns recent activity, newest first. With a root configured,
// only file activities inside it are returned. since is the id of the last
// activity already seen (0 for the newest).
func (c *Client) Activities(ctx context.Context, limit int, since int64) ([]Activity, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if since > 0 {
		query.Set("since", strconv.FormatInt(since, 10))
	}
	var raw []struct {
		ID         int64  `json:"activity_id"`
		Type       string `json:"type"`
		User       string `json:"user"`
		Subject    string `json:"subject"`
		ObjectType string `json:"object_type"`
		ObjectName string `json:"object_name"`
		DateTime   string `json:"datetime"`
	}
	err := c.ocs(ctx, http.MethodGet, "/ocs/v2.php/apps/activity/api/v2/activity?"+query.Encode(), nil, &raw)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Status == http.StatusNotFound {
			return nil, Errorf("The Activity app is not enabled on this Nextcloud.")
		}
		return nil, err
	}
	var activities []Activity
	for _, item := range raw {
		activity := Activity{ID: item.ID, Type: item.Type, User: item.User, Subject: item.Subject, Time: item.DateTime}
		if item.ObjectType == "files" && item.ObjectName != "" {
			rel, inside := c.Rel("/" + strings.Trim(item.ObjectName, "/"))
			if !inside {
				continue
			}
			activity.Path = rel
		} else if c.cfg.Root != "" {
			continue // not about a file: cannot be shown to be inside the root
		}
		activities = append(activities, activity)
	}
	return activities, nil
}
