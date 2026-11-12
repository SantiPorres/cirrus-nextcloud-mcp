// Package sharing implements the "sharing" tool group. It is off by default
// and needs both the group and NEXTCLOUD_ALLOW_SHARE to be enabled.
package sharing

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const minPasswordChars = 10

type group struct {
	client *nextcloud.Client
	cfg    *config.Config
	now    func() time.Time
}

// Register adds the sharing tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client, cfg: r.Config, now: time.Now}

	var policy []string
	if r.Config.ShareRequirePassword {
		policy = append(policy, "a password of at least "+strconv.Itoa(minPasswordChars)+" characters is required")
	}
	if r.Config.ShareRequireExpiry {
		policy = append(policy, "an expiry date is required")
	}
	policy = append(policy, "the expiry date can be at most "+strconv.Itoa(r.Config.ShareMaxExpiryDays)+" days ahead")

	tools.Add(r, tools.Spec{
		Name: "nc_shares_list", Title: "List shares", Group: config.GroupSharing, Permission: tools.Share,
		Description: "Lists the shares created by this account: public links and shares with users. With \"target\", " +
			"only the shares of that file or folder. Passwords are never returned.",
	}, g.list)

	tools.Add(r, tools.Spec{
		Name: "nc_share_create", Title: "Create share", Group: config.GroupSharing, Permission: tools.Share,
		Description: "Shares a file or folder, either as a public link (type \"link\") or with a Nextcloud user " +
			"(type \"user\"). For public links: " + strings.Join(policy, "; ") + ". Shares are read-only unless " +
			"permissions=\"edit\"; the right to re-share is never granted. Tell the person the password through a " +
			"different channel than the link.",
	}, g.create)

	tools.Add(r, tools.Spec{
		Name: "nc_share_delete", Title: "Delete share", Group: config.GroupSharing, Permission: tools.Share, Destructive: true,
		Description: "Stops sharing: the link stops working or the user loses access. The file itself is not touched.",
	}, g.delete)
}

func shareMap(share nextcloud.Share) map[string]any {
	kind := "other"
	switch share.Type {
	case nextcloud.ShareTypeLink:
		kind = "link"
	case nextcloud.ShareTypeUser:
		kind = "user"
	}
	permissions := "read"
	if share.Permissions&(nextcloud.PermissionUpdate|nextcloud.PermissionCreate|nextcloud.PermissionDelete) != 0 {
		permissions = "edit"
	}
	out := map[string]any{"share_id": share.ID, "type": kind, "path": share.Path, "permissions": permissions}
	if share.Type != nextcloud.ShareTypeLink {
		// Nextcloud sets this bit on every public link by itself; it only means something for people.
		out["can_reshare"] = share.Permissions&nextcloud.PermissionShare != 0
	}
	if share.IsFolder {
		out["item"] = "folder"
	} else {
		out["item"] = "file"
	}
	if share.Type == nextcloud.ShareTypeLink {
		out["url"], out["password_protected"] = share.URL, share.HasPassword
	}
	if share.ShareWith != "" {
		out["share_with"] = share.ShareWith
	}
	if share.Label != "" {
		out["label"] = share.Label
	}
	if share.Expiration != "" {
		out["expires"] = share.Expiration
	}
	if share.Created > 0 {
		out["created"] = time.Unix(share.Created, 0).UTC().Format(time.RFC3339)
	}
	return out
}

type listInput struct {
	Target string `json:"target,omitempty" jsonschema:"Optional file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
}

func (g *group) list(ctx context.Context, in listInput) (any, error) {
	var target *nextcloud.Target
	if strings.TrimSpace(in.Target) != "" {
		resolved, err := g.client.Resolve(ctx, in.Target, false)
		if err != nil {
			return nil, err
		}
		target = &resolved
	}
	shares, err := g.client.Shares(ctx, target)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(shares))
	for _, share := range shares {
		list = append(list, shareMap(share))
	}
	return map[string]any{"count": len(list), "shares": list}, nil
}

type createInput struct {
	Target      string `json:"target" jsonschema:"The file or folder to share: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Type        string `json:"type" jsonschema:"\"link\" for a public link, or \"user\" to share with a Nextcloud user."`
	ShareWith   string `json:"share_with,omitempty" jsonschema:"For type \"user\": the user id to share with."`
	Password    string `json:"password,omitempty" jsonschema:"For type \"link\": the password people must enter to open the link."`
	Expires     string `json:"expires,omitempty" jsonschema:"Last day the share works, as YYYY-MM-DD."`
	Permissions string `json:"permissions,omitempty" jsonschema:"\"read\" (default) or \"edit\"."`
	Label       string `json:"label,omitempty" jsonschema:"Optional note shown next to a public link in Nextcloud."`
}

// expiry validates an expiry date against the policy and returns it normalised.
func (g *group) expiry(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	limit := g.cfg.ShareMaxExpiryDays
	today := g.now().UTC().Truncate(24 * time.Hour)
	if raw == "" {
		if required {
			return "", nextcloud.Errorf("expires is required for public links: pass a date at most %d days ahead, as YYYY-MM-DD (the latest allowed is %s).",
				limit, today.AddDate(0, 0, limit).Format(time.DateOnly))
		}
		return "", nil
	}
	date, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return "", nextcloud.Errorf("expires must be a date written as YYYY-MM-DD.")
	}
	if !date.After(today) {
		return "", nextcloud.Errorf("expires must be a future date.")
	}
	if latest := today.AddDate(0, 0, limit); date.After(latest) {
		return "", nextcloud.Errorf("expires can be at most %d days ahead (%s).", limit, latest.Format(time.DateOnly))
	}
	return date.Format(time.DateOnly), nil
}

func (g *group) create(ctx context.Context, in createInput) (any, error) {
	request := nextcloud.ShareRequest{Label: strings.TrimSpace(in.Label)}
	var err error
	switch strings.ToLower(strings.TrimSpace(in.Type)) {
	case "link":
		request.Type = nextcloud.ShareTypeLink
		if strings.TrimSpace(in.ShareWith) != "" {
			return nil, nextcloud.Errorf("share_with is only for type \"user\".")
		}
		request.Password = in.Password
		if g.cfg.ShareRequirePassword && request.Password == "" {
			return nil, nextcloud.Errorf("password is required for public links (at least %d characters).", minPasswordChars)
		}
		if request.Password != "" && utf8.RuneCountInString(request.Password) < minPasswordChars {
			return nil, nextcloud.Errorf("The password must have at least %d characters.", minPasswordChars)
		}
		if request.ExpireDate, err = g.expiry(in.Expires, g.cfg.ShareRequireExpiry); err != nil {
			return nil, err
		}
	case "user":
		request.Type = nextcloud.ShareTypeUser
		request.ShareWith = strings.TrimSpace(in.ShareWith)
		if request.ShareWith == "" {
			return nil, nextcloud.Errorf("share_with is required for type \"user\".")
		}
		if in.Password != "" {
			return nil, nextcloud.Errorf("password is only for type \"link\".")
		}
		if request.ExpireDate, err = g.expiry(in.Expires, false); err != nil {
			return nil, err
		}
	default:
		return nil, nextcloud.Errorf("type must be \"link\" or \"user\".")
	}

	target, err := g.client.Resolve(ctx, in.Target, false)
	if err != nil {
		return nil, err
	}
	if target.Rel == "/" {
		return nil, nextcloud.Errorf("The top folder cannot be shared. Share a file or a folder inside it.")
	}
	entry, err := g.client.Stat(ctx, target)
	if err != nil {
		return nil, err
	}
	request.Target = target

	switch strings.ToLower(strings.TrimSpace(in.Permissions)) {
	case "", "read":
		request.Permissions = nextcloud.PermissionRead
	case "edit":
		request.Permissions = nextcloud.PermissionRead | nextcloud.PermissionUpdate
		if entry.IsFolder {
			request.Permissions |= nextcloud.PermissionCreate | nextcloud.PermissionDelete
		}
	default:
		return nil, nextcloud.Errorf("permissions must be \"read\" or \"edit\".")
	}

	share, err := g.client.CreateShare(ctx, request)
	if err != nil {
		return nil, err
	}
	out := shareMap(share)
	out["status"] = "created"
	return out, nil
}

type deleteInput struct {
	ShareID string `json:"share_id" jsonschema:"The \"share_id\" returned by nc_shares_list or nc_share_create."`
}

func (g *group) delete(ctx context.Context, in deleteInput) (any, error) {
	share, err := g.client.DeleteShare(ctx, strings.TrimSpace(in.ShareID))
	if err != nil {
		return nil, err
	}
	out := shareMap(share)
	out["status"] = "share deleted"
	return out, nil
}
