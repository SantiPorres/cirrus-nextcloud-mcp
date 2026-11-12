// Package diagnostics implements the "diagnostics" tool group.
package diagnostics

import (
	"context"
	"time"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

// Register adds get_server_info. Call it after every other group, so that the
// registry already knows which tools exist.
func Register(r *tools.Registry) {
	tools.Add(r, tools.Spec{
		Name: "get_server_info", Title: "Server information", Group: config.GroupDiagnostics,
		Description: "Describes the server that is answering: version and commit, start time, enabled tool groups, " +
			"the tools it exposes, the tools that are switched off and why, and the configuration flags. Use it to " +
			"tell an old deployment from a client with an outdated tool list. It never returns credentials, the " +
			"Nextcloud address or the account name. With check_connection=true it also reports whether Nextcloud " +
			"is reachable and its version.",
	}, func(ctx context.Context, in input) (any, error) { return Info(ctx, r, in.CheckConnection), nil })
}

type input struct {
	CheckConnection bool `json:"check_connection,omitempty" jsonschema:"Also contact Nextcloud and report whether it answers, its version and whether the trash bin and versions are enabled. Defaults to false."`
}

// Info builds the server description. It contains no secrets, no host names and no paths.
func Info(ctx context.Context, r *tools.Registry, checkConnection bool) map[string]any {
	cfg := r.Config
	active := r.Active()

	var disabledGroups []string
	for _, name := range config.AllGroups {
		if !cfg.Groups[name] {
			disabledGroups = append(disabledGroups, name)
		}
	}
	info := map[string]any{
		"server":            r.Build.Name,
		"version":           r.Build.Version,
		"commit":            r.Build.Commit,
		"started_at":        r.Build.StartedAt.UTC().Format(time.RFC3339),
		"transport":         cfg.Transport,
		"tool_groups":       map[string]any{"enabled": cfg.EnabledGroups(), "disabled": emptyIfNil(disabledGroups)},
		"tool_count":        len(active),
		"tools":             active,
		"unavailable_tools": r.Skipped(),
		"permissions": map[string]any{
			"write": cfg.AllowWrite, "delete": cfg.AllowDelete, "share": cfg.AllowShare,
		},
		"config": map[string]any{
			"root_restricted":               cfg.Root != "",
			"folder_creation_enabled":       cfg.FolderWritePrefix != "",
			"editable_extensions":           cfg.SortedExtensions(),
			"editable_spreadsheet_patterns": len(cfg.EditableSpreadsheets),
			"protected_sheets":              len(cfg.ProtectedSheets),
			"share_requires_password":       cfg.ShareRequirePassword,
			"share_requires_expiry":         cfg.ShareRequireExpiry,
			"share_max_expiry_days":         cfg.ShareMaxExpiryDays,
			"max_download_bytes":            cfg.MaxDownloadBytes,
			"max_text_bytes":                cfg.MaxTextBytes,
			"tls_verification":              "always on",
			"insecure_http":                 cfg.BaseURL.Scheme == "http",
			"follows_redirects":             false,
		},
	}
	if checkConnection {
		caps, err := r.Client.Capabilities(ctx)
		if err != nil {
			info["nextcloud"] = map[string]any{"reachable": false, "error": r.Client.Scrub(err.Error())}
		} else {
			info["nextcloud"] = map[string]any{
				"reachable": true, "version": caps.Version, "trash_enabled": caps.Trash, "versioning_enabled": caps.Versioning,
			}
		}
	}
	return info
}

func emptyIfNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
