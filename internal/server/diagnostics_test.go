package server_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

func TestServerInfo(t *testing.T) {
	h := newHarness(t, map[string]string{
		"NEXTCLOUD_ROOT": "/Very/Private/Folder", "NEXTCLOUD_ALLOW_WRITE": "true",
		"NEXTCLOUD_EDITABLE_SPREADSHEETS": "confidential-name*.xlsx",
	})
	info := h.call("get_server_info", nil)
	if info["server"] != "cirrus-nextcloud-mcp" || info["version"] != "test" || info["commit"] != "abc123" || info["started_at"] == "" {
		t.Errorf("identity = %v", info)
	}
	names := h.toolNames()
	slices.Sort(names)
	var reported []string
	for _, name := range info["tools"].([]any) {
		reported = append(reported, name.(string))
	}
	if !slices.Equal(names, reported) || info["tool_count"].(float64) != float64(len(names)) {
		t.Errorf("reported tools %v differ from the real list %v", reported, names)
	}
	unavailable := info["unavailable_tools"].(map[string]any)
	if !strings.Contains(unavailable["nc_delete"].(string), "NEXTCLOUD_ALLOW_DELETE") ||
		!strings.Contains(unavailable["nc_create_folder"].(string), "NEXTCLOUD_FOLDER_WRITE_PREFIX") {
		t.Errorf("unavailable = %v", unavailable)
	}
	permissions := info["permissions"].(map[string]any)
	config := info["config"].(map[string]any)
	if permissions["write"] != true || permissions["delete"] != false || permissions["share"] != false ||
		config["root_restricted"] != true || config["folder_creation_enabled"] != false ||
		config["editable_spreadsheet_patterns"].(float64) != 1 || config["follows_redirects"] != false {
		t.Errorf("flags = %v %v", permissions, config)
	}
	groups := info["tool_groups"].(map[string]any)
	if !slices.Contains(groups["disabled"].([]any), any("sharing")) || !slices.Contains(groups["enabled"].([]any), any("files")) {
		t.Errorf("groups = %v", groups)
	}
	if _, present := info["nextcloud"]; present {
		t.Error("Nextcloud must only be contacted on request")
	}

	// Nothing identifying or secret may appear anywhere in the output.
	raw, _ := json.Marshal(info)
	for _, forbidden := range []string{fakedav.Password, fakedav.User, strings.TrimPrefix(h.fake.URL, "http://"), "Very/Private", "confidential-name"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("get_server_info leaks %q", forbidden)
		}
	}
}

func TestServerInfoConnectionCheck(t *testing.T) {
	h := newHarness(t, nil)
	nextcloud := h.call("get_server_info", args("check_connection", true))["nextcloud"].(map[string]any)
	if nextcloud["reachable"] != true || nextcloud["version"] != "33.0.0" || nextcloud["trash_enabled"] != true {
		t.Errorf("nextcloud = %v", nextcloud)
	}

	bad := newHarness(t, map[string]string{"NEXTCLOUD_APP_PASSWORD": "wrong-password-value"})
	result := bad.raw("get_server_info", args("check_connection", true))
	if result.IsError {
		t.Fatal("a failed connection check is information, not a tool error")
	}
	if got := text(result); !strings.Contains(got, `"reachable":false`) || strings.Contains(got, "wrong-password-value") {
		t.Errorf("result = %s", got)
	}
}

func TestServerInfoRespectsItsGroup(t *testing.T) {
	names := newHarness(t, map[string]string{"NEXTCLOUD_TOOL_GROUPS": "files"}).toolNames()
	if slices.Contains(names, "get_server_info") {
		t.Error("get_server_info belongs to the diagnostics group")
	}
}
