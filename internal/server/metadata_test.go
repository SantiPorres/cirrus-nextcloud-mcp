package server_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

func TestMetadataPermissions(t *testing.T) {
	readOnly := newHarness(t, nil).toolNames()
	withWrite := newHarness(t, writable).toolNames()
	for _, name := range []string{"nc_tags_list", "nc_tag_search", "nc_favorites_list", "nc_comments_list", "nc_activity_list", "nc_recent_files", "nc_user_info"} {
		if !slices.Contains(readOnly, name) {
			t.Errorf("%s should be available on a read-only server", name)
		}
	}
	for _, name := range []string{"nc_tag_create", "nc_tag_assign", "nc_tag_remove", "nc_favorite_set", "nc_comment_add"} {
		if slices.Contains(readOnly, name) || !slices.Contains(withWrite, name) {
			t.Errorf("%s must depend on NEXTCLOUD_ALLOW_WRITE", name)
		}
	}
}

func TestTags(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ROOT": "/Team"})
	id := h.fake.AddFile("/Team/a.md", []byte("a"))
	h.fake.AddFile("/Team/Sub/b.md", []byte("b"))
	outside := h.fake.AddFile("/Private/c.md", []byte("c"))

	urgent := h.call("nc_tag_create", args("name", " Urgent "))
	if urgent["name"] != "Urgent" || urgent["id"] == "" {
		t.Fatalf("created = %v", urgent)
	}
	h.call("nc_tag_create", args("name", "archive"))
	h.fails("nc_tag_create", args("name", "Urgent"), "already exists")
	h.fails("nc_tag_create", args("name", "  "), "between 1 and 64")
	if all := h.call("nc_tags_list", nil); all["count"].(float64) != 2 || all["tags"].([]any)[0].(map[string]any)["name"] != "archive" {
		t.Errorf("tags = %v", all)
	}

	if assigned := h.call("nc_tag_assign", args("target", fmt.Sprintf("%s/f/%d", h.fake.URL, id), "tag", "URGENT")); assigned["status"] != "assigned" || assigned["path"] != "/a.md" {
		t.Errorf("assigned = %v", assigned)
	}
	if again := h.call("nc_tag_assign", args("target", "/a.md", "tag", urgent["id"])); again["status"] != "already assigned" {
		t.Errorf("again = %v", again)
	}
	h.call("nc_tag_assign", args("target", "/Sub", "tag", "urgent"))
	h.fails("nc_tag_assign", args("target", "/a.md", "tag", "missing"), "There is no tag")
	h.fails("nc_tag_assign", args("target", "/nope.md", "tag", "urgent"), "Not found")
	h.fails("nc_tag_assign", args("target", "/../Private/c.md", "tag", "urgent"), "'..'")
	h.fails("nc_tag_assign", args("target", fmt.Sprintf("%s/f/%d", h.fake.URL, outside), "tag", "urgent"), "not found")

	if ofFile := h.call("nc_tags_list", args("target", "/a.md")); ofFile["count"].(float64) != 1 || ofFile["tags"].([]any)[0].(map[string]any)["name"] != "Urgent" {
		t.Errorf("tags of file = %v", ofFile)
	}
	found := h.call("nc_tag_search", args("tag", "urgent"))
	if found["count"].(float64) != 2 || found["results"].([]any)[0].(map[string]any)["path"] != "/a.md" {
		t.Errorf("found = %v", found)
	}
	if limited := h.call("nc_tag_search", args("tag", "urgent", "limit", 1)); limited["count"].(float64) != 1 || limited["total"].(float64) != 2 {
		t.Errorf("limited = %v", limited)
	}

	if removed := h.call("nc_tag_remove", args("target", "/a.md", "tag", "Urgent")); removed["status"] != "removed" {
		t.Errorf("removed = %v", removed)
	}
	if again := h.call("nc_tag_remove", args("target", "/a.md", "tag", "Urgent")); again["status"] != "was not assigned" {
		t.Errorf("again = %v", again)
	}
	if h.file("/Team/a.md") != "a" {
		t.Error("tagging touched the file")
	}
}

func TestTagSearchStaysInsideTheRoot(t *testing.T) {
	// The tag is put on a file outside the root by a server that can see everything.
	open := newHarness(t, writable)
	open.fake.AddFile("/Private/secret.md", []byte("s"))
	open.call("nc_tag_create", args("name", "shared-tag"))
	open.call("nc_tag_assign", args("target", "/Private/secret.md", "tag", "shared-tag"))
	if count := open.call("nc_tag_search", args("tag", "shared-tag"))["count"].(float64); count != 1 {
		t.Fatalf("precondition: %v", count)
	}
	for _, request := range open.fake.Requests() {
		if request.Method == "REPORT" && request.Path != "/remote.php/dav/files/"+fakedav.User {
			t.Errorf("unexpected REPORT scope %s", request.Path)
		}
	}

	restricted := newHarness(t, map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	restricted.fake.AddFile("/Team/x.md", []byte("x"))
	restricted.call("nc_favorites_list", nil)
	for _, request := range restricted.fake.Requests() {
		if request.Method == "REPORT" && request.Path != "/remote.php/dav/files/"+fakedav.User+"/Team" {
			t.Errorf("the REPORT must be scoped to the root, got %s", request.Path)
		}
	}
}

func TestFavorites(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ROOT": "/Team"})
	h.fake.AddFile("/Team/a.md", []byte("a"))
	h.fake.AddFile("/Team/b.md", []byte("b"))

	if set := h.call("nc_favorite_set", args("target", "/a.md")); set["favorite"] != true {
		t.Errorf("set = %v", set)
	}
	favorites := h.call("nc_favorites_list", nil)
	if favorites["count"].(float64) != 1 || favorites["items"].([]any)[0].(map[string]any)["path"] != "/a.md" {
		t.Errorf("favorites = %v", favorites)
	}
	if info := h.call("nc_get_file_info", args("target", "/a.md")); info["favorite"] != true {
		t.Errorf("info = %v", info)
	}
	h.call("nc_favorite_set", args("target", "/a.md", "favorite", false))
	if favorites := h.call("nc_favorites_list", nil); favorites["count"].(float64) != 0 {
		t.Errorf("after unset = %v", favorites)
	}
	h.fails("nc_favorite_set", args("target", "/"), "top folder")
	h.fails("nc_favorite_set", args("target", "/missing.md"), "Not found")
}

func TestComments(t *testing.T) {
	h := newHarness(t, writable)
	h.fake.AddFile("/doc.md", []byte("d"))
	for _, message := range []string{"first <b>&</b>", "second", "third"} {
		if added := h.call("nc_comment_add", args("target", "/doc.md", "message", message)); added["status"] != "comment added" {
			t.Errorf("added = %v", added)
		}
	}
	page := h.call("nc_comments_list", args("target", "/doc.md", "limit", 2))
	list := page["comments"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["message"] != "third" || list[0].(map[string]any)["author"] != fakedav.User || page["next_offset"].(float64) != 2 {
		t.Errorf("page = %v", page)
	}
	page = h.call("nc_comments_list", args("target", "/doc.md", "limit", 2, "offset", 2))
	if list := page["comments"].([]any); len(list) != 1 || list[0].(map[string]any)["message"] != "first <b>&</b>" {
		t.Errorf("second page = %v", page)
	}
	h.fails("nc_comment_add", args("target", "/doc.md", "message", ""), "between 1 and 1000")
	h.fails("nc_comment_add", args("target", "/doc.md", "message", strings.Repeat("x", 1001)), "between 1 and 1000")
	h.fails("nc_comment_add", args("target", "/missing.md", "message", "x"), "Not found")
}

func TestRecentFilesAndUser(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	h.fake.AddFile("/Team/old.md", []byte("o"))
	h.fake.AddFile("/Team/Sub/new.md", []byte("n"))
	h.fake.AddFile("/Private/newest.md", []byte("p"))
	h.fake.SetModified("/Team/old.md", time.Now().AddDate(0, 0, -30))
	h.fake.SetModified("/Team/Sub/new.md", time.Now().Add(-time.Hour))

	recent := h.call("nc_recent_files", nil)
	files := recent["files"].([]any)
	if recent["days"].(float64) != 7 || len(files) != 1 || files[0].(map[string]any)["path"] != "/Sub/new.md" {
		t.Errorf("recent = %v", recent)
	}
	if wide := h.call("nc_recent_files", args("days", 60)); wide["count"].(float64) != 2 || wide["files"].([]any)[0].(map[string]any)["path"] != "/Sub/new.md" {
		t.Errorf("60 days = %v", wide)
	}

	account := h.call("nc_user_info", nil)
	quota := account["quota"].(map[string]any)
	if account["id"] != fakedav.User || account["display_name"] != "Alice Example" || quota["used_bytes"].(float64) != 100 || quota["total_bytes"].(float64) != 1000 {
		t.Errorf("account = %v", account)
	}
	if _, present := account["email"]; present {
		t.Error("an unset e-mail must be omitted")
	}
}

func TestActivityIsFilteredToTheRoot(t *testing.T) {
	restricted := newHarness(t, map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	if empty := restricted.call("nc_activity_list", nil); empty["count"].(float64) != 0 {
		t.Errorf("an empty stream = %v", empty)
	}
	stream := []fakedav.FakeActivity{
		{Type: "file_created", Subject: "You created a.md", ObjectType: "files", ObjectName: "/Team/a.md"},
		{Type: "file_changed", Subject: "You changed secret.md", ObjectType: "files", ObjectName: "/Private/secret.md"},
		{Type: "systemtags", Subject: "You created system tag X", ObjectType: "systemtag", ObjectName: "X"},
		{Type: "file_deleted", Subject: "You deleted b.md", ObjectType: "files", ObjectName: "/Team/Sub/b.md"},
	}
	for _, activity := range stream {
		restricted.fake.AddActivity(activity)
	}
	listed := restricted.call("nc_activity_list", nil)
	items := listed["activities"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["path"] != "/Sub/b.md" || items[1].(map[string]any)["path"] != "/a.md" {
		t.Errorf("activities = %v", listed)
	}
	if strings.Contains(text(restricted.raw("nc_activity_list", nil)), "secret") {
		t.Error("activity outside the root leaked")
	}
	older := restricted.call("nc_activity_list", args("since_id", items[0].(map[string]any)["activity_id"]))
	if older["count"].(float64) != 1 {
		t.Errorf("older = %v", older)
	}

	open := newHarness(t, nil)
	for _, activity := range stream {
		open.fake.AddActivity(activity)
	}
	if all := open.call("nc_activity_list", args("limit", 3)); all["count"].(float64) != 3 {
		t.Errorf("without a root everything is visible, up to the limit: %v", all)
	}
}
