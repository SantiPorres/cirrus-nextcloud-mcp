package nextcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Share types of the Nextcloud sharing API.
const (
	ShareTypeUser = 0
	ShareTypeLink = 3
)

// Share permission bits.
const (
	PermissionRead   = 1
	PermissionUpdate = 2
	PermissionCreate = 4
	PermissionDelete = 8
	PermissionShare  = 16
)

// Share is a share of a file or folder inside the root.
type Share struct {
	ID          string
	Type        int
	Path        string // relative to the root
	IsFolder    bool
	ShareWith   string
	URL         string
	Label       string
	Permissions int
	Expiration  string
	HasPassword bool
	Created     int64
}

type rawShare struct {
	ID          json.RawMessage `json:"id"`
	ShareType   int             `json:"share_type"`
	Path        string          `json:"path"`
	ItemType    string          `json:"item_type"`
	ShareWith   *string         `json:"share_with"`
	ShareWithDN *string         `json:"share_with_displayname"`
	URL         string          `json:"url"`
	Label       string          `json:"label"`
	Permissions int             `json:"permissions"`
	Expiration  *string         `json:"expiration"`
	Password    *string         `json:"password"`
	STime       int64           `json:"stime"`
}

// convert maps an API share to a Share; false when it is outside the root.
func (c *Client) convertShare(raw rawShare) (Share, bool) {
	rel, inside := c.Rel("/" + strings.Trim(raw.Path, "/"))
	if !inside {
		return Share{}, false
	}
	share := Share{
		ID: strings.Trim(string(raw.ID), `"`), Type: raw.ShareType, Path: rel, IsFolder: raw.ItemType == "folder",
		URL: raw.URL, Label: raw.Label, Permissions: raw.Permissions, Created: raw.STime,
	}
	if raw.Expiration != nil {
		share.Expiration, _, _ = strings.Cut(*raw.Expiration, " ")
	}
	if raw.ShareType == ShareTypeLink {
		// For links, share_with holds the password hash: never pass it on.
		share.HasPassword = (raw.ShareWith != nil && *raw.ShareWith != "") || (raw.Password != nil && *raw.Password != "")
	} else if raw.ShareWith != nil {
		share.ShareWith = *raw.ShareWith
	}
	return share, true
}

const sharesAPI = "/ocs/v2.php/apps/files_sharing/api/v1/shares"

// Shares lists the account's shares inside the root; with a target, only the shares of that item.
func (c *Client) Shares(ctx context.Context, target *Target) ([]Share, error) {
	query := url.Values{}
	if target != nil {
		query.Set("path", absOrRoot(target.Abs))
		query.Set("reshares", "false")
	}
	var raw []rawShare
	if err := c.ocs(ctx, http.MethodGet, sharesAPI+"?"+query.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	shares := []Share{}
	for _, item := range raw {
		if share, inside := c.convertShare(item); inside {
			shares = append(shares, share)
		}
	}
	return shares, nil
}

func absOrRoot(abs string) string {
	if abs == "" {
		return "/"
	}
	return abs
}

// ShareRequest describes a share to create.
type ShareRequest struct {
	Target      Target
	Type        int
	ShareWith   string
	Password    string
	ExpireDate  string // YYYY-MM-DD, or ""
	Permissions int
	Label       string
}

// CreateShare creates a share. The password is sent to Nextcloud and never returned or logged.
func (c *Client) CreateShare(ctx context.Context, request ShareRequest) (Share, error) {
	form := url.Values{
		"path":        {absOrRoot(request.Target.Abs)},
		"shareType":   {strconv.Itoa(request.Type)},
		"permissions": {strconv.Itoa(request.Permissions)},
	}
	if request.ShareWith != "" {
		form.Set("shareWith", request.ShareWith)
	}
	if request.Password != "" {
		form.Set("password", request.Password)
	}
	if request.ExpireDate != "" {
		form.Set("expireDate", request.ExpireDate)
	}
	if request.Label != "" {
		form.Set("label", request.Label)
	}
	var raw rawShare
	if err := c.ocs(ctx, http.MethodPost, sharesAPI, form, &raw); err != nil {
		if request.Password != "" {
			if typed, ok := err.(*Error); ok { //nolint:errorlint // ocs returns *Error directly
				typed.Message = strings.ReplaceAll(typed.Message, request.Password, "[redacted]")
			}
		}
		return Share{}, err
	}
	share, inside := c.convertShare(raw)
	if !inside {
		return Share{}, Errorf("Nextcloud created a share for an unexpected path.")
	}
	return share, nil
}

// GetShare returns one share, if it belongs to an item inside the root.
func (c *Client) GetShare(ctx context.Context, id string) (Share, error) {
	if !numericID.MatchString(id) {
		return Share{}, Errorf("share_id is not valid. Call nc_shares_list for the current ids.")
	}
	var raw []rawShare
	if err := c.ocs(ctx, http.MethodGet, sharesAPI+"/"+id, nil, &raw); err != nil {
		if IsNotFound(err) {
			return Share{}, Errorf("There is no share %s. Call nc_shares_list for the current ids.", id)
		}
		return Share{}, err
	}
	for _, item := range raw {
		if share, inside := c.convertShare(item); inside {
			return share, nil
		}
	}
	return Share{}, Errorf("There is no share %s. Call nc_shares_list for the current ids.", id)
}

// DeleteShare stops sharing. The shared file itself is not touched.
func (c *Client) DeleteShare(ctx context.Context, id string) (Share, error) {
	share, err := c.GetShare(ctx, id)
	if err != nil {
		return Share{}, err
	}
	return share, c.ocs(ctx, http.MethodDelete, sharesAPI+"/"+id, nil, nil)
}
