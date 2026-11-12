// Package safeops implements the "safe_ops" tool group: moving, copying,
// deleting to the trash, and restoring from the trash or from versions.
// None of these tools can destroy data permanently or replace a file blindly.
package safeops

import (
	"context"
	"strings"
	"time"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const (
	defaultTrashLimit = 50
	maxTrashLimit     = 500
)

type group struct {
	client *nextcloud.Client
}

// Register adds the safe file operation tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client}

	tools.Add(r, tools.Spec{
		Name: "nc_move", Title: "Move or rename", Group: config.GroupSafeOps, Permission: tools.Write,
		Description: "Moves or renames a file or folder. \"destination\" is the complete new path (including the " +
			"new name), not the folder to move into. It never replaces anything: if the destination exists the " +
			"call fails. The destination's parent folder must exist.",
	}, g.move)

	tools.Add(r, tools.Spec{
		Name: "nc_copy", Title: "Copy", Group: config.GroupSafeOps, Permission: tools.Write,
		Description: "Copies a file or folder. \"destination\" is the complete path of the copy (including its name). " +
			"It never replaces anything: if the destination exists the call fails. The destination's parent " +
			"folder must exist.",
	}, g.copy)

	tools.Add(r, tools.Spec{
		Name: "nc_delete", Title: "Delete to trash", Group: config.GroupSafeOps, Permission: tools.Delete, Destructive: true,
		Description: "Deletes a file or folder by moving it to the Nextcloud trash bin, from where it can be restored " +
			"with nc_trash_restore. Nothing is ever deleted permanently: the call is refused when the trash bin " +
			"is not available. Returns the \"trash_id\" needed to restore it.",
	}, g.delete)

	tools.Add(r, tools.Spec{
		Name: "nc_trash_list", Title: "List trash", Group: config.GroupSafeOps,
		Description: "Lists deleted files and folders that can still be restored, newest first, with the " +
			"\"trash_id\" that nc_trash_restore needs. The trash cannot be emptied from here.",
	}, g.trashList)

	tools.Add(r, tools.Spec{
		Name: "nc_trash_restore", Title: "Restore from trash", Group: config.GroupSafeOps, Permission: tools.Write,
		Description: "Restores a deleted file or folder to its original location. If something else now exists " +
			"there, Nextcloud keeps both by giving the restored item a different name.",
	}, g.trashRestore)

	tools.Add(r, tools.Spec{
		Name: "nc_versions_list", Title: "List file versions", Group: config.GroupSafeOps,
		Description: "Lists the previous versions Nextcloud keeps of a file, newest first, with the \"version_id\" " +
			"that nc_versions_restore needs and the file's \"current_etag\".",
	}, g.versionsList)

	tools.Add(r, tools.Spec{
		Name: "nc_versions_restore", Title: "Restore file version", Group: config.GroupSafeOps, Permission: tools.Write, Destructive: true,
		Description: "Makes a previous version the current content of a file. Pass the \"current_etag\" returned by " +
			"nc_versions_list: the restore is refused if the file changed since. Nextcloud keeps the replaced " +
			"content as another version, so the restore can itself be undone.",
	}, g.versionsRestore)
}

type transferInput struct {
	Source      string `json:"source" jsonschema:"The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	Destination string `json:"destination" jsonschema:"Complete new path relative to the top folder, including the name, e.g. \"/Archive/2025 report.md\"."`
}

func (g *group) transfer(ctx context.Context, in transferInput, run func(context.Context, nextcloud.Target, nextcloud.Target) error, status string) (any, error) {
	source, err := g.client.Resolve(ctx, in.Source, false)
	if err != nil {
		return nil, err
	}
	destination, err := g.client.PathTarget(in.Destination, false)
	if err != nil {
		return nil, err
	}
	if err := run(ctx, source, destination); err != nil {
		return nil, err
	}
	return map[string]any{"from": source.Rel, "to": destination.Rel, "status": status}, nil
}

func (g *group) move(ctx context.Context, in transferInput) (any, error) {
	return g.transfer(ctx, in, g.client.Move, "moved")
}

func (g *group) copy(ctx context.Context, in transferInput) (any, error) {
	return g.transfer(ctx, in, g.client.Copy, "copied")
}

type targetInput struct {
	Target string `json:"target" jsonschema:"A path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
}

func (g *group) delete(ctx context.Context, in targetInput) (any, error) {
	target, err := g.client.Resolve(ctx, in.Target, false)
	if err != nil {
		return nil, err
	}
	if target.Rel == "/" {
		return nil, nextcloud.Errorf("The top folder cannot be deleted.")
	}
	entry, err := g.client.Stat(ctx, target)
	if err != nil {
		return nil, err
	}
	// Asked every time, not cached: this answer is what keeps the delete reversible.
	caps, err := g.client.FreshCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !caps.Trash {
		return nil, nextcloud.Errorf("The trash bin is not enabled on this Nextcloud, so deleting would be permanent. Nothing was deleted.")
	}
	if err := g.client.Delete(ctx, target); err != nil {
		return nil, err
	}

	out := map[string]any{"path": target.Rel, "type": kind(entry.IsFolder), "status": "moved to trash"}
	items, err := g.client.TrashList(ctx)
	if err == nil {
		for _, item := range items {
			if (entry.FileID != "" && item.FileID == entry.FileID) || (entry.FileID == "" && item.OriginalPath == target.Rel) {
				out["trash_id"] = item.ID
				return out, nil
			}
		}
	}
	out["status"] = "deleted"
	out["warning"] = "The item was deleted but could not be found in the trash afterwards. Check the trash bin in Nextcloud."
	return out, nil
}

func kind(isFolder bool) string {
	if isFolder {
		return "folder"
	}
	return "file"
}

type trashListInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of items (1-500). Defaults to 50."`
}

func trashMap(item nextcloud.TrashItem) map[string]any {
	out := map[string]any{
		"trash_id": item.ID, "name": item.Name, "original_path": item.OriginalPath, "type": kind(item.IsFolder),
	}
	if item.DeletedAt > 0 {
		out["deleted_at"] = time.Unix(item.DeletedAt, 0).UTC().Format(time.RFC3339)
	}
	if item.Size >= 0 {
		out["size_bytes"] = item.Size
	}
	return out
}

func (g *group) trashList(ctx context.Context, in trashListInput) (any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultTrashLimit
	}
	limit = min(limit, maxTrashLimit)
	items, err := g.client.TrashList(ctx)
	if err != nil {
		return nil, err
	}
	total := len(items)
	if total > limit {
		items = items[:limit]
	}
	list := make([]map[string]any, 0, len(items))
	for _, item := range items {
		list = append(list, trashMap(item))
	}
	return map[string]any{"total": total, "count": len(list), "items": list}, nil
}

type trashRestoreInput struct {
	TrashID string `json:"trash_id" jsonschema:"The \"trash_id\" returned by nc_trash_list or nc_delete."`
}

func (g *group) trashRestore(ctx context.Context, in trashRestoreInput) (any, error) {
	id := strings.TrimSpace(in.TrashID)
	if id == "" || strings.ContainsAny(id, "/\\") {
		return nil, nextcloud.Errorf("trash_id is not valid. Call nc_trash_list for the current ids.")
	}
	item, err := g.client.TrashRestore(ctx, id)
	if err != nil {
		return nil, err
	}
	out := trashMap(item)
	out["status"] = "restored"
	out["note"] = "Restored to original_path, or next to it under a different name if that path was taken."
	return out, nil
}

func (g *group) versionsList(ctx context.Context, in targetInput) (any, error) {
	entry, err := g.statFile(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	versions, err := g.client.Versions(ctx, entry.FileID)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		item := map[string]any{"version_id": version.ID, "modified": version.Modified}
		if version.Size >= 0 {
			item["size_bytes"] = version.Size
		}
		if version.Label != "" {
			item["label"] = version.Label
		}
		if version.Author != "" {
			item["author"] = version.Author
		}
		list = append(list, item)
	}
	return map[string]any{
		"path": entry.Path, "current_etag": entry.ETag, "current_modified": entry.Modified,
		"count": len(list), "versions": list,
	}, nil
}

func (g *group) statFile(ctx context.Context, rawTarget string) (nextcloud.Entry, error) {
	target, err := g.client.Resolve(ctx, rawTarget, true)
	if err != nil {
		return nextcloud.Entry{}, err
	}
	entry, err := g.client.Stat(ctx, target)
	if err != nil {
		return nextcloud.Entry{}, err
	}
	if entry.IsFolder {
		return nextcloud.Entry{}, nextcloud.Errorf("%s is a folder; only files have versions.", entry.Path)
	}
	return entry, nil
}

type versionsRestoreInput struct {
	Target    string `json:"target" jsonschema:"The file: a path relative to the top folder, or an internal link (https://<host>/f/<id>)."`
	VersionID string `json:"version_id" jsonschema:"The \"version_id\" returned by nc_versions_list."`
	ETag      string `json:"etag" jsonschema:"The \"current_etag\" returned by nc_versions_list, proving the file has not changed since."`
}

func normalizeETag(etag string) string { return strings.Trim(strings.TrimSpace(etag), `"`) }

func (g *group) versionsRestore(ctx context.Context, in versionsRestoreInput) (any, error) {
	if normalizeETag(in.ETag) == "" {
		return nil, nextcloud.Errorf("etag is required: pass the current_etag returned by nc_versions_list.")
	}
	entry, err := g.statFile(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	if normalizeETag(entry.ETag) != normalizeETag(in.ETag) {
		return nil, nextcloud.Errorf("%s was modified since its versions were listed. Nothing was restored; call nc_versions_list again.", entry.Path)
	}
	if err := g.client.RestoreVersion(ctx, entry.FileID, strings.TrimSpace(in.VersionID)); err != nil {
		return nil, err
	}
	out := map[string]any{"path": entry.Path, "restored_version": strings.TrimSpace(in.VersionID), "status": "restored"}
	if after, err := g.client.Stat(ctx, entry.Target()); err == nil {
		out["etag"] = after.ETag
		out["size_bytes"] = after.Size
	}
	return out, nil
}
