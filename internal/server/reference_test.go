package server_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/pdfrender"
)

const referencePath = "../../docs/tools.md"

// everythingOn enables every group and permission, so that every tool is registered.
var everythingOn = map[string]string{
	"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ALLOW_DELETE": "true", "NEXTCLOUD_ALLOW_SHARE": "true",
	"NEXTCLOUD_TOOL_GROUPS":           "files,sheets,safe_ops,binary,metadata,sharing,diagnostics",
	"NEXTCLOUD_FOLDER_WRITE_PREFIX":   "/Projects",
	"NEXTCLOUD_EDITABLE_SPREADSHEETS": "*.xlsx",
}

func renderReference(tools []*mcp.Tool) string {
	var b strings.Builder
	b.WriteString("# Tool reference\n\n")
	b.WriteString("<!-- Generated from the server's own tool list. Do not edit by hand:\n")
	b.WriteString("     run `UPDATE_DOCS=1 go test ./internal/server -run TestToolReference` instead. -->\n\n")
	b.WriteString("Every tool the server can register, with all groups and permissions enabled, the folder prefix set to\n")
	b.WriteString("`/Projects` and the spreadsheet pattern `*.xlsx`. A real server only exposes the tools its configuration allows;\n")
	b.WriteString("`get_server_info` lists them and explains why the others are missing.\n")
	for _, tool := range tools {
		access := "changes data"
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			access = "read-only"
		}
		fmt.Fprintf(&b, "\n## `%s`\n\n%s\n\n*%s*\n", tool.Name, tool.Description, access)

		var schema struct {
			Properties map[string]struct {
				Type        any    `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		raw, _ := json.Marshal(tool.InputSchema)
		_ = json.Unmarshal(raw, &schema)
		if len(schema.Properties) == 0 {
			b.WriteString("\nNo parameters.\n")
			continue
		}
		names := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			ri, rj := slices.Contains(schema.Required, names[i]), slices.Contains(schema.Required, names[j])
			if ri != rj {
				return ri
			}
			return names[i] < names[j]
		})
		b.WriteString("\n| Parameter | Type | Required | Description |\n|---|---|---|---|\n")
		for _, name := range names {
			property := schema.Properties[name]
			kind := "any"
			switch t := property.Type.(type) {
			case string:
				kind = t
			case []any:
				parts := make([]string, 0, len(t))
				for _, part := range t {
					if part != "null" {
						parts = append(parts, fmt.Sprint(part))
					}
				}
				kind = strings.Join(parts, " or ")
			}
			required := "no"
			if slices.Contains(schema.Required, name) {
				required = "yes"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", name, kind, required, strings.ReplaceAll(property.Description, "|", "\\|"))
		}
	}
	return b.String()
}

// TestToolReference keeps docs/tools.md identical to what the server really exposes.
func TestToolReference(t *testing.T) {
	if pdfrender.Find() == "" {
		t.Skip("pdftoppm is not installed, so nc_render_pdf is not registered and the reference would be incomplete")
	}
	h := newHarness(t, everythingOn)
	listed, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(listed.Tools, func(i, j int) bool { return listed.Tools[i].Name < listed.Tools[j].Name })
	want := renderReference(listed.Tools)

	if os.Getenv("UPDATE_DOCS") != "" {
		if err := os.WriteFile(referencePath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(referencePath)
	if err != nil || string(got) != want {
		t.Errorf("docs/tools.md is out of date. Run: UPDATE_DOCS=1 go test ./internal/server -run TestToolReference")
	}
}

// TestEveryToolIsDescribed guards the quality of what the model sees.
func TestEveryToolIsDescribed(t *testing.T) {
	h := newHarness(t, everythingOn)
	listed, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := 43; len(listed.Tools) != want && pdfrender.Find() != "" {
		t.Errorf("%d tools are registered with everything enabled, expected %d", len(listed.Tools), want)
	}
	for _, tool := range listed.Tools {
		if len(tool.Description) < 40 || tool.Title == "" {
			t.Errorf("%s needs a title and a real description", tool.Name)
		}
		if tool.Name != "get_server_info" && !strings.HasPrefix(tool.Name, "nc_") {
			t.Errorf("%s does not follow the nc_ naming", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(raw, &schema)
		for name, property := range schema.Properties {
			if property.Description == "" {
				t.Errorf("%s: parameter %s has no description", tool.Name, name)
			}
		}
	}
}
