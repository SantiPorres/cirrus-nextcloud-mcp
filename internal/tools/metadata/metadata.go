// Package metadata implements the "metadata" tool group: system tags,
// favorites, file comments, activity, recent files and account information.
package metadata

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const maxCommentChars = 1000

type group struct {
	client *nextcloud.Client
}

// Register adds the metadata tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client}
	add := func(name, title string, permission tools.Permission, description string) tools.Spec {
		return tools.Spec{Name: name, Title: title, Group: config.GroupMetadata, Permission: permission, Description: description}
	}

	tools.Add(r, add("nc_tags_list", "List tags", tools.Read,
		"Lists the system tags of this Nextcloud. With \"target\", lists only the tags assigned to that file or folder."), g.tagsList)
	tools.Add(r, add("nc_tag_search", "Find files by tag", tools.Read,
		"Lists the files and folders that carry a system tag, given by name or id."), g.tagSearch)
	tools.Add(r, add("nc_tag_create", "Create tag", tools.Write,
		"Creates a new system tag, visible to and assignable by users. Tags are shared by the whole Nextcloud instance."), g.tagCreate)
	assign := add("nc_tag_assign", "Assign tag", tools.Write,
		"Assigns an existing system tag (by name or id) to a file or folder. Assigning a tag twice has no further effect.")
	assign.Idempotent = true
	tools.Add(r, assign, g.tagAssign)
	remove := add("nc_tag_remove", "Remove tag", tools.Write,
		"Removes a system tag from a file or folder. The tag itself and the file are not deleted.")
	remove.Idempotent = true
	tools.Add(r, remove, g.tagRemove)

	tools.Add(r, add("nc_favorites_list", "List favorites", tools.Read,
		"Lists the files and folders marked as favorite."), g.favoritesList)
	favorite := add("nc_favorite_set", "Set favorite", tools.Write,
		"Marks a file or folder as favorite, or removes the mark with favorite=false.")
	favorite.Idempotent = true
	tools.Add(r, favorite, g.favoriteSet)

	tools.Add(r, add("nc_comments_list", "List file comments", tools.Read,
		"Lists the comments people left on a file or folder, newest first."), g.commentsList)
	tools.Add(r, add("nc_comment_add", "Add file comment", tools.Write,
		"Adds a comment to a file or folder, as the account this server uses. Comments are visible to everyone with access to the file."), g.commentAdd)

	tools.Add(r, add("nc_activity_list", "List activity", tools.Read,
		"Lists recent activity of the account (who changed, shared or deleted what), newest first. When the server "+
			"is restricted to a folder, only file activity inside it is returned. Pass the smallest \"activity_id\" of a "+
			"page as since_id to get older entries."), g.activityList)
	tools.Add(r, add("nc_recent_files", "List recent files", tools.Read,
		"Lists the files modified in the last days, newest first."), g.recentFiles)
	tools.Add(r, add("nc_user_info", "Account information", tools.Read,
		"Returns the Nextcloud account this server is connected as (id, display name, e-mail, groups) and its storage quota."), g.userInfo)
}

func (g *group) stat(ctx context.Context, rawTarget string) (nextcloud.Entry, error) {
	target, err := g.client.Resolve(ctx, rawTarget, false)
	if err != nil {
		return nextcloud.Entry{}, err
	}
	return g.client.Stat(ctx, target)
}

func tagMaps(tags []nextcloud.Tag) []map[string]any {
	out := make([]map[string]any, 0, len(tags))
	for _, tag := range tags {
		out = append(out, map[string]any{"id": tag.ID, "name": tag.Name, "assignable": tag.Assignable})
	}
	return out
}

// --- tags -------------------------------------------------------------------

type tagsListInput struct {
	Target string `json:"target,omitempty" jsonschema:"Optional file or folder whose tags to list: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
}

func (g *group) tagsList(ctx context.Context, in tagsListInput) (any, error) {
	if strings.TrimSpace(in.Target) == "" {
		tags, err := g.client.Tags(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(tags), "tags": tagMaps(tags)}, nil
	}
	entry, err := g.stat(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	tags, err := g.client.FileTags(ctx, entry.FileID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": entry.Path, "count": len(tags), "tags": tagMaps(tags)}, nil
}

type tagSearchInput struct {
	Tag   string `json:"tag" jsonschema:"Tag name (case-insensitive) or id."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum number of results (1-500). Defaults to 50."`
}

func clamp(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

func (g *group) tagSearch(ctx context.Context, in tagSearchInput) (any, error) {
	tag, err := g.client.FindTag(ctx, in.Tag)
	if err != nil {
		return nil, err
	}
	entries, err := g.client.FilesWithTag(ctx, tag.ID)
	if err != nil {
		return nil, err
	}
	total := len(entries)
	if limit := clamp(in.Limit, 50, 500); total > limit {
		entries = entries[:limit]
	}
	return map[string]any{"tag": tag.Name, "tag_id": tag.ID, "total": total, "count": len(entries), "results": tools.EntryMaps(g.client, entries)}, nil
}

type tagCreateInput struct {
	Name string `json:"name" jsonschema:"Name of the new tag."`
}

func (g *group) tagCreate(ctx context.Context, in tagCreateInput) (any, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, nextcloud.Errorf("name must have between 1 and 64 characters.")
	}
	tag, err := g.client.CreateTag(ctx, name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": tag.ID, "name": tag.Name, "status": "created"}, nil
}

type tagTargetInput struct {
	Target string `json:"target" jsonschema:"The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Tag    string `json:"tag" jsonschema:"Tag name (case-insensitive) or id. The tag must already exist."`
}

func (g *group) tagChange(ctx context.Context, in tagTargetInput, change func(context.Context, string, string) (bool, error), done, noop string) (any, error) {
	entry, err := g.stat(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	tag, err := g.client.FindTag(ctx, in.Tag)
	if err != nil {
		return nil, err
	}
	changed, err := change(ctx, entry.FileID, tag.ID)
	if err != nil {
		return nil, err
	}
	status := noop
	if changed {
		status = done
	}
	return map[string]any{"path": entry.Path, "tag": tag.Name, "tag_id": tag.ID, "status": status}, nil
}

func (g *group) tagAssign(ctx context.Context, in tagTargetInput) (any, error) {
	return g.tagChange(ctx, in, g.client.AssignTag, "assigned", "already assigned")
}

func (g *group) tagRemove(ctx context.Context, in tagTargetInput) (any, error) {
	return g.tagChange(ctx, in, g.client.RemoveTag, "removed", "was not assigned")
}

// --- favorites --------------------------------------------------------------

type emptyInput struct{}

func (g *group) favoritesList(ctx context.Context, _ emptyInput) (any, error) {
	entries, err := g.client.Favorites(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(entries), "items": tools.EntryMaps(g.client, entries)}, nil
}

type favoriteInput struct {
	Target   string `json:"target" jsonschema:"The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Favorite *bool  `json:"favorite,omitempty" jsonschema:"true to mark as favorite, false to remove the mark. Defaults to true."`
}

func (g *group) favoriteSet(ctx context.Context, in favoriteInput) (any, error) {
	target, err := g.client.Resolve(ctx, in.Target, false)
	if err != nil {
		return nil, err
	}
	if target.Rel == "/" {
		return nil, nextcloud.Errorf("The top folder cannot be marked as favorite from here.")
	}
	favorite := in.Favorite == nil || *in.Favorite
	if err := g.client.SetFavorite(ctx, target, favorite); err != nil {
		return nil, err
	}
	return map[string]any{"path": target.Rel, "favorite": favorite}, nil
}

// --- comments ---------------------------------------------------------------

type commentsListInput struct {
	Target string `json:"target" jsonschema:"The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum number of comments (1-100). Defaults to 20."`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of comments to skip. Defaults to 0."`
}

func (g *group) commentsList(ctx context.Context, in commentsListInput) (any, error) {
	entry, err := g.stat(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	limit := clamp(in.Limit, 20, 100)
	comments, err := g.client.Comments(ctx, entry.FileID, limit, max(0, in.Offset))
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(comments))
	for _, comment := range comments {
		list = append(list, map[string]any{
			"id": comment.ID, "author": comment.Author, "author_name": comment.Name, "created": comment.Created, "message": comment.Message,
		})
	}
	out := map[string]any{"path": entry.Path, "count": len(list), "comments": list}
	if len(list) == limit {
		out["next_offset"] = max(0, in.Offset) + limit
	}
	return out, nil
}

type commentAddInput struct {
	Target  string `json:"target" jsonschema:"The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Message string `json:"message" jsonschema:"The comment text (at most 1000 characters)."`
}

func (g *group) commentAdd(ctx context.Context, in commentAddInput) (any, error) {
	message := strings.TrimSpace(in.Message)
	if message == "" || utf8.RuneCountInString(message) > maxCommentChars {
		return nil, nextcloud.Errorf("message must have between 1 and %d characters.", maxCommentChars)
	}
	entry, err := g.stat(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	if err := g.client.AddComment(ctx, entry.FileID, message); err != nil {
		return nil, err
	}
	return map[string]any{"path": entry.Path, "status": "comment added"}, nil
}

// --- activity, recent files, user ---------------------------------------------

type activityInput struct {
	Limit   int   `json:"limit,omitempty" jsonschema:"Maximum number of entries to fetch (1-200). Defaults to 30."`
	SinceID int64 `json:"since_id,omitempty" jsonschema:"Return entries older than this activity_id. Defaults to the newest."`
}

func (g *group) activityList(ctx context.Context, in activityInput) (any, error) {
	activities, err := g.client.Activities(ctx, clamp(in.Limit, 30, 200), max(0, in.SinceID))
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(activities))
	for _, activity := range activities {
		item := map[string]any{
			"activity_id": activity.ID, "type": activity.Type, "user": activity.User, "subject": activity.Subject, "time": activity.Time,
		}
		if activity.Path != "" {
			item["path"] = activity.Path
		}
		list = append(list, item)
	}
	return map[string]any{"count": len(list), "activities": list}, nil
}

type recentInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of files (1-100). Defaults to 25."`
	Days  int `json:"days,omitempty" jsonschema:"How many days back to look (1-365). Defaults to 7."`
}

func (g *group) recentFiles(ctx context.Context, in recentInput) (any, error) {
	days := clamp(in.Days, 7, 365)
	entries, err := g.client.RecentFiles(ctx, time.Now().AddDate(0, 0, -days), clamp(in.Limit, 25, 100))
	if err != nil {
		return nil, err
	}
	return map[string]any{"days": days, "count": len(entries), "files": tools.EntryMaps(g.client, entries)}, nil
}

func (g *group) userInfo(ctx context.Context, _ emptyInput) (any, error) {
	user, err := g.client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	quota := map[string]any{"used_bytes": user.QuotaUsed}
	if user.QuotaTotal >= 0 {
		quota["total_bytes"], quota["free_bytes"], quota["used_percent"] = user.QuotaTotal, user.QuotaFree, user.QuotaRelative
	} else {
		quota["unlimited"] = true
	}
	out := map[string]any{"id": user.ID, "display_name": user.DisplayName, "groups": user.Groups, "language": user.Language, "quota": quota}
	if user.Email != "" {
		out["email"] = user.Email
	}
	if user.Groups == nil {
		out["groups"] = []string{}
	}
	return out, nil
}
