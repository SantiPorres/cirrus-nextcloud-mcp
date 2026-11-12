//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTagsFavoritesAndComments(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true"})
	h.put("/tagged.md", "x")
	h.put("/plain.md", "y")
	tagName := fmt.Sprintf("it-tag-%d", time.Now().UnixNano())

	created := h.call("nc_tag_create", args("name", tagName))
	if created["id"] == "" || created["name"] != tagName {
		t.Fatalf("created = %v", created)
	}
	h.fails("nc_tag_create", args("name", tagName), "already exists")
	if !strings.Contains(text(h.raw("nc_tags_list", nil)), tagName) {
		t.Error("the new tag is not listed")
	}

	if assigned := h.call("nc_tag_assign", args("target", "/tagged.md", "tag", strings.ToUpper(tagName))); assigned["status"] != "assigned" {
		t.Errorf("assigned = %v", assigned)
	}
	if again := h.call("nc_tag_assign", args("target", "/tagged.md", "tag", created["id"])); again["status"] != "already assigned" {
		t.Errorf("again = %v", again)
	}
	h.fails("nc_tag_assign", args("target", "/tagged.md", "tag", "no-such-tag-anywhere"), "There is no tag")
	if ofFile := h.call("nc_tags_list", args("target", "/tagged.md")); ofFile["count"].(float64) != 1 {
		t.Errorf("tags of the file = %v", ofFile)
	}
	found := h.call("nc_tag_search", args("tag", tagName))
	if found["count"].(float64) != 1 || found["results"].([]any)[0].(map[string]any)["path"] != "/tagged.md" {
		t.Errorf("found = %v", found)
	}
	if removed := h.call("nc_tag_remove", args("target", "/tagged.md", "tag", tagName)); removed["status"] != "removed" {
		t.Errorf("removed = %v", removed)
	}
	if again := h.call("nc_tag_remove", args("target", "/tagged.md", "tag", tagName)); again["status"] != "was not assigned" {
		t.Errorf("again = %v", again)
	}
	if found := h.call("nc_tag_search", args("tag", tagName)); found["count"].(float64) != 0 {
		t.Errorf("after removal = %v", found)
	}

	h.call("nc_favorite_set", args("target", "/tagged.md"))
	favorites := h.call("nc_favorites_list", nil)
	if favorites["count"].(float64) != 1 || favorites["items"].([]any)[0].(map[string]any)["path"] != "/tagged.md" {
		t.Errorf("favorites = %v", favorites)
	}
	if info := h.call("nc_get_file_info", args("target", "/tagged.md")); info["favorite"] != true {
		t.Errorf("info = %v", info)
	}
	h.call("nc_favorite_set", args("target", "/tagged.md", "favorite", false))
	if favorites := h.call("nc_favorites_list", nil); favorites["count"].(float64) != 0 {
		t.Errorf("favorites after unset = %v", favorites)
	}

	h.call("nc_comment_add", args("target", "/tagged.md", "message", "First comment <b>&</b> ñ"))
	h.call("nc_comment_add", args("target", "/tagged.md", "message", "Second comment"))
	comments := h.call("nc_comments_list", args("target", "/tagged.md"))
	list := comments["comments"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["message"] != "Second comment" ||
		list[1].(map[string]any)["message"] != "First comment <b>&</b> ñ" || list[0].(map[string]any)["author"] != user {
		t.Errorf("comments = %v", comments)
	}
	h.fails("nc_comment_add", args("target", "/tagged.md", "message", "  "), "between 1 and")
}

func TestRecentFilesActivityAndUser(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true"})
	h.put("/fresh.md", "new")

	recent := h.call("nc_recent_files", args("days", 1))
	files := recent["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "/fresh.md" {
		t.Errorf("recent files must stay inside the root: %v", recent)
	}

	account := h.call("nc_user_info", nil)
	if account["id"] != user || account["quota"].(map[string]any)["used_bytes"] == nil {
		t.Errorf("account = %v", account)
	}

	activity := h.call("nc_activity_list", args("limit", 50))
	for _, raw := range activity["activities"].([]any) {
		item := raw.(map[string]any)
		path, _ := item["path"].(string)
		if path == "" || strings.Contains(path, "mcp-it-") {
			t.Errorf("an activity outside the root was returned: %v", item)
		}
	}
}

func TestSharing(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_SHARE": "true"})
	h.put("/shared.md", "content")
	soon := time.Now().UTC().AddDate(0, 0, 7).Format(time.DateOnly)
	password := "Tr0ub4dor&3-horse-staple"

	h.fails("nc_share_create", args("target", "/shared.md", "type", "link", "expires", soon), "password is required")
	h.fails("nc_share_create", args("target", "/shared.md", "type", "link", "password", password), "expires is required")
	h.fails("nc_share_create", args("target", "/shared.md", "type", "link", "password", password,
		"expires", time.Now().UTC().AddDate(0, 0, 90).Format(time.DateOnly)), "at most 30 days")
	if count := h.call("nc_shares_list", args("target", "/shared.md"))["count"].(float64); count != 0 {
		t.Fatalf("a refused share was created (%v)", count)
	}

	result := h.raw("nc_share_create", args("target", "/shared.md", "type", "link", "password", password, "expires", soon, "label", "for review"))
	if result.IsError {
		t.Fatalf("create: %s", text(result))
	}
	if strings.Contains(text(result), password) {
		t.Error("the password was echoed back")
	}
	created := h.call("nc_shares_list", args("target", "/shared.md"))["shares"].([]any)[0].(map[string]any)
	if created["type"] != "link" || created["password_protected"] != true || created["expires"] != soon ||
		created["permissions"] != "read" || !strings.Contains(created["url"].(string), "/s/") {
		t.Errorf("share = %v", created)
	}
	if strings.Contains(text(h.raw("nc_shares_list", nil)), "$argon") || strings.Contains(text(h.raw("nc_shares_list", nil)), "|$") {
		t.Error("the password hash is exposed")
	}

	// Another server restricted elsewhere cannot see or delete it.
	other := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_SHARE": "true"})
	if count := other.call("nc_shares_list", nil)["count"].(float64); count != 0 {
		t.Errorf("shares of another root are visible (%v)", count)
	}
	other.fails("nc_share_delete", args("share_id", created["share_id"]), "There is no share")

	if deleted := h.call("nc_share_delete", args("share_id", created["share_id"])); deleted["status"] != "share deleted" {
		t.Errorf("deleted = %v", deleted)
	}
	if count := h.call("nc_shares_list", args("target", "/shared.md"))["count"].(float64); count != 0 {
		t.Errorf("the share still exists (%v)", count)
	}
	if h.get("/shared.md") != "content" {
		t.Error("deleting the share touched the file")
	}
	h.fails("nc_share_create", args("target", "/shared.md", "type", "user", "share_with", "nobody-like-this"), "")
}
