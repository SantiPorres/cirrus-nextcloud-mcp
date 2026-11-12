package server_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

var shareEnv = map[string]string{"NEXTCLOUD_ALLOW_SHARE": "true", "NEXTCLOUD_TOOL_GROUPS": "files,sharing", "NEXTCLOUD_ROOT": "/Team"}

const strongPassword = "correct-horse-battery"

func inDays(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format(time.DateOnly) }

func TestSharingIsOffByDefault(t *testing.T) {
	shareTools := []string{"nc_shares_list", "nc_share_create", "nc_share_delete"}
	cases := map[string]map[string]string{
		"defaults":         nil,
		"permission only":  {"NEXTCLOUD_ALLOW_SHARE": "true"},
		"group only":       {"NEXTCLOUD_TOOL_GROUPS": "files,sharing"},
		"write and delete": {"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ALLOW_DELETE": "true", "NEXTCLOUD_TOOL_GROUPS": "files,sharing"},
	}
	for name, env := range cases {
		names := newHarness(t, env).toolNames()
		for _, tool := range shareTools {
			if slices.Contains(names, tool) {
				t.Errorf("%s: %s must not exist", name, tool)
			}
		}
	}
	names := newHarness(t, shareEnv).toolNames()
	for _, tool := range shareTools {
		if !slices.Contains(names, tool) {
			t.Errorf("%s should exist when both the group and the permission are on", tool)
		}
	}
	if slices.Contains(names, "nc_create_file") {
		t.Error("the share permission must not grant write tools")
	}
}

func TestPublicLinksNeedPasswordAndExpiry(t *testing.T) {
	h := newHarness(t, shareEnv)
	h.fake.AddFile("/Team/report.md", []byte("r"))
	h.fake.AddFile("/Team/Folder/x.md", []byte("x"))
	link := func(extra ...any) map[string]any {
		return args(append([]any{"target", "/report.md", "type", "link"}, extra...)...)
	}

	h.fails("nc_share_create", link("expires", inDays(7)), "password is required")
	h.fails("nc_share_create", link("password", "short", "expires", inDays(7)), "at least 10 characters")
	h.fails("nc_share_create", link("password", strongPassword), "expires is required")
	h.fails("nc_share_create", link("password", strongPassword, "expires", inDays(31)), "at most 30 days")
	h.fails("nc_share_create", link("password", strongPassword, "expires", inDays(0)), "future date")
	h.fails("nc_share_create", link("password", strongPassword, "expires", "next week"), "YYYY-MM-DD")
	h.fails("nc_share_create", link("password", strongPassword, "expires", inDays(7), "share_with", "bob"), "only for type")
	h.fails("nc_share_create", link("password", strongPassword, "expires", inDays(7), "permissions", "admin"), "\"read\" or \"edit\"")
	h.fails("nc_share_create", args("target", "/report.md", "type", "email"), "\"link\" or \"user\"")
	h.fails("nc_share_create", args("target", "/", "type", "link", "password", strongPassword, "expires", inDays(7)), "top folder")
	h.fails("nc_share_create", args("target", "/missing.md", "type", "link", "password", strongPassword, "expires", inDays(7)), "Not found")
	h.fails("nc_share_create", args("target", "/../Private/x", "type", "link", "password", strongPassword, "expires", inDays(7)), "'..'")
	if len(h.fake.Shares()) != 0 {
		t.Fatalf("a refused share was created: %+v", h.fake.Shares())
	}

	result := h.raw("nc_share_create", link("password", strongPassword, "expires", inDays(30), "label", "for review"))
	if result.IsError {
		t.Fatalf("create failed: %s", text(result))
	}
	if strings.Contains(text(result), strongPassword) || strings.Contains(text(result), "argon") {
		t.Errorf("the password or its hash was returned: %s", text(result))
	}
	stored := h.fake.Shares()[0]
	if stored.Path != "/Team/report.md" || stored.Type != 3 || stored.Password != strongPassword || stored.Expiration != inDays(30) || stored.Permissions != 1 {
		t.Errorf("stored share = %+v", stored)
	}

	// Edit on a folder grants create and delete inside it, never re-sharing.
	h.call("nc_share_create", args("target", "/Folder", "type", "link", "password", strongPassword, "expires", inDays(1), "permissions", "edit"))
	if folder := h.fake.Shares()[1]; folder.Permissions != 1|2|4|8 {
		t.Errorf("folder permissions = %d", folder.Permissions)
	}
	// What Nextcloud says about a rejected password must not echo the password.
	h.fails("nc_share_create", link("password", "password-too-common", "expires", inDays(7)), "among the most common")
	if message := text(h.raw("nc_share_create", link("password", "password-too-common", "expires", inDays(7)))); strings.Contains(message, "password-too-common") {
		t.Errorf("the rejected password was echoed: %s", message)
	}
}

func TestSharePolicyIsConfigurable(t *testing.T) {
	h := newHarness(t, map[string]string{
		"NEXTCLOUD_ALLOW_SHARE": "true", "NEXTCLOUD_TOOL_GROUPS": "sharing",
		"NEXTCLOUD_SHARE_REQUIRE_PASSWORD": "false", "NEXTCLOUD_SHARE_REQUIRE_EXPIRY": "false", "NEXTCLOUD_SHARE_MAX_EXPIRY_DAYS": "3",
	})
	h.fake.AddFile("/a.md", []byte("a"))
	created := h.call("nc_share_create", args("target", "/a.md", "type", "link"))
	if created["password_protected"] != false || created["status"] != "created" {
		t.Errorf("created = %v", created)
	}
	if _, present := created["expires"]; present {
		t.Error("no expiry was requested")
	}
	h.fails("nc_share_create", args("target", "/a.md", "type", "link", "expires", inDays(4)), "at most 3 days")
}

func TestUserShares(t *testing.T) {
	h := newHarness(t, shareEnv)
	h.fake.AddFile("/Team/report.md", []byte("r"))

	created := h.call("nc_share_create", args("target", "/report.md", "type", "user", "share_with", "bob", "permissions", "edit"))
	if created["type"] != "user" || created["share_with"] != "bob" || created["permissions"] != "edit" || created["can_reshare"] != false {
		t.Errorf("created = %v", created)
	}
	if stored := h.fake.Shares()[0]; stored.Permissions != 1|2 || stored.Password != "" {
		t.Errorf("stored = %+v", stored)
	}
	h.fails("nc_share_create", args("target", "/report.md", "type", "user"), "share_with is required")
	h.fails("nc_share_create", args("target", "/report.md", "type", "user", "share_with", "bob", "password", strongPassword), "only for type")
	h.fails("nc_share_create", args("target", "/report.md", "type", "user", "share_with", "nobody"), "valid account")
	h.fails("nc_share_create", args("target", "/report.md", "type", "user", "share_with", "bob", "expires", inDays(90)), "at most 30 days")
}

func TestListAndDeleteSharesStayInsideTheRoot(t *testing.T) {
	h := newHarness(t, shareEnv)
	h.fake.AddFile("/Team/report.md", []byte("r"))
	h.fake.AddFile("/Team/other.md", []byte("o"))
	h.fake.AddFile("/Private/secret.md", []byte("s"))
	foreign := h.fake.AddShare(fakedav.FakeShare{Type: 3, Path: "/Private/secret.md", Password: "x", Permissions: 17})
	h.call("nc_share_create", args("target", "/report.md", "type", "link", "password", strongPassword, "expires", inDays(5)))

	listed := h.call("nc_shares_list", nil)
	shares := listed["shares"].([]any)
	if len(shares) != 1 {
		t.Fatalf("shares = %v", listed)
	}
	share := shares[0].(map[string]any)
	if share["path"] != "/report.md" || share["password_protected"] != true || share["expires"] != inDays(5) || share["item"] != "file" ||
		!strings.Contains(share["url"].(string), "/s/") {
		t.Errorf("share = %v", share)
	}
	if raw := text(h.raw("nc_shares_list", nil)); strings.Contains(raw, "argon") || strings.Contains(raw, "secret") {
		t.Errorf("the listing leaks a hash or a path outside the root: %s", raw)
	}
	if ofOther := h.call("nc_shares_list", args("target", "/other.md")); ofOther["count"].(float64) != 0 {
		t.Errorf("shares of another file = %v", ofOther)
	}

	h.fails("nc_share_delete", args("share_id", strconv.Itoa(foreign)), "There is no share")
	h.fails("nc_share_delete", args("share_id", "999"), "There is no share")
	h.fails("nc_share_delete", args("share_id", "1/../2"), "not valid")
	if deleted := h.call("nc_share_delete", args("share_id", share["share_id"])); deleted["status"] != "share deleted" {
		t.Errorf("deleted = %v", deleted)
	}
	remaining := h.fake.Shares()
	if len(remaining) != 1 || remaining[0].Path != "/Private/secret.md" || h.file("/Team/report.md") != "r" {
		t.Errorf("remaining = %+v", remaining)
	}
}
