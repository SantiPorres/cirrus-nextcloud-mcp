// Package tools holds what every tool group shares: the registry that decides
// which tools exist for a given configuration, and the conversion of handler
// results and errors into MCP results.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
)

// Permission is what a tool needs to be allowed to do.
type Permission int

// Permissions, from least to most sensitive.
const (
	Read Permission = iota
	Write
	Delete
	Share
)

// Spec describes one tool.
type Spec struct {
	Name        string
	Title       string
	Description string
	// Group is one of the config.Group* constants.
	Group      string
	Permission Permission
	// Destructive marks write tools that replace or remove existing data
	// (as opposed to only adding new data).
	Destructive bool
	// Idempotent marks tools that can be repeated without further effect.
	Idempotent bool
	// Unavailable, when not empty, is the reason this tool cannot be offered
	// with the current configuration.
	Unavailable string
}

// Build describes the running server, for diagnostics.
type Build struct {
	Name      string
	Version   string
	Commit    string
	StartedAt time.Time
}

// Registry adds tools to an MCP server, leaving out those the configuration
// does not allow. A tool that is left out is invisible to the client.
type Registry struct {
	Server *mcp.Server
	Config *config.Config
	Client *nextcloud.Client
	Logger *slog.Logger
	Build  Build

	active  []string
	skipped map[string]string
}

// NewRegistry builds an empty registry.
func NewRegistry(server *mcp.Server, cfg *config.Config, client *nextcloud.Client, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Registry{Server: server, Config: cfg, Client: client, Logger: logger, skipped: map[string]string{}}
}

// Active returns the names of the registered tools, sorted.
func (r *Registry) Active() []string {
	out := append([]string(nil), r.active...)
	sort.Strings(out)
	return out
}

// Skipped returns the tools that were left out, with the reason for each.
func (r *Registry) Skipped() map[string]string {
	out := make(map[string]string, len(r.skipped))
	for name, reason := range r.skipped {
		out[name] = reason
	}
	return out
}

func (r *Registry) unavailable(spec Spec) string {
	if !r.Config.Groups[spec.Group] {
		return "tool group '" + spec.Group + "' is not in NEXTCLOUD_TOOL_GROUPS"
	}
	switch spec.Permission {
	case Write:
		if !r.Config.AllowWrite {
			return "NEXTCLOUD_ALLOW_WRITE is not true"
		}
	case Delete:
		if !r.Config.AllowDelete {
			return "NEXTCLOUD_ALLOW_DELETE is not true"
		}
	case Share:
		if !r.Config.AllowShare {
			return "NEXTCLOUD_ALLOW_SHARE is not true"
		}
	}
	return spec.Unavailable
}

// Handler is a tool implementation. It returns either a JSON-serialisable
// value (normally a map) or a ready *mcp.CallToolResult for tools that
// return images or embedded resources.
type Handler[In any] func(ctx context.Context, in In) (any, error)

// Add registers a tool if the configuration allows it. The input schema is
// inferred from In: fields tagged `json:",omitempty"` are optional and the
// `jsonschema` tag is the parameter description.
func Add[In any](r *Registry, spec Spec, handler Handler[In]) {
	if reason := r.unavailable(spec); reason != "" {
		r.skipped[spec.Name] = reason
		return
	}
	r.active = append(r.active, spec.Name)

	readOnly := spec.Permission == Read
	destructive := spec.Destructive
	closedWorld := false
	tool := &mcp.Tool{
		Name:        spec.Name,
		Title:       spec.Title,
		Description: spec.Description,
		Annotations: &mcp.ToolAnnotations{
			Title:           spec.Title,
			ReadOnlyHint:    readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  spec.Idempotent || readOnly,
			OpenWorldHint:   &closedWorld,
		},
	}
	mcp.AddTool(r.Server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := handler(ctx, in)
		if err != nil {
			message := r.Client.Scrub(err.Error())
			r.Logger.Warn("tool failed", "tool", spec.Name, "error", message)
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: message}},
			}, nil, nil
		}
		if result, ok := out.(*mcp.CallToolResult); ok {
			return result, nil, nil
		}
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(out); err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "The result could not be encoded."}},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(bytes.TrimSpace(buf.Bytes()))}},
		}, nil, nil
	})
}

// EntryMap is the public form of a file or folder in tool results.
func EntryMap(c *nextcloud.Client, e nextcloud.Entry) map[string]any {
	kind := "file"
	if e.IsFolder {
		kind = "folder"
	}
	out := map[string]any{
		"name":     e.Name,
		"path":     e.Path,
		"type":     kind,
		"modified": e.Modified,
	}
	if e.Size >= 0 {
		out["size_bytes"] = e.Size
	}
	if e.MIMEType != "" {
		out["mime_type"] = e.MIMEType
	}
	if link := c.FileLink(e.FileID); link != "" {
		out["url"] = link
	}
	return out
}

// EntryMaps converts a list of entries.
func EntryMaps(c *nextcloud.Client, entries []nextcloud.Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, EntryMap(c, e))
	}
	return out
}
