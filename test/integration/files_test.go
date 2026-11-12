//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

var writable = map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_FOLDER_WRITE_PREFIX": "/Work"}

func TestBrowseAndRead(t *testing.T) {
	h := newHarness(t, nil)
	dav(t, "MKCOL", h.root+"/Specs", "", nil)
	h.put("/Specs/Plan año 2026.md", "línea 1\nlínea 2\n")
	h.put("/zeta.txt", "z")

	listing := h.call("nc_list_files", nil)
	items := listing["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["name"] != "Specs" || items[0].(map[string]any)["type"] != "folder" {
		t.Fatalf("listing = %v", listing)
	}

	found := h.call("nc_search_files", args("query", "plan año"))
	if found["count"].(float64) != 1 {
		t.Fatalf("search = %v", found)
	}
	hit := found["results"].([]any)[0].(map[string]any)
	if hit["path"] != "/Specs/Plan año 2026.md" {
		t.Errorf("paths must be relative to the root: %v", hit)
	}
	link, _ := hit["url"].(string)
	if !strings.Contains(link, "/f/") {
		t.Fatalf("no file link in %v", hit)
	}

	info := h.call("nc_get_file_info", args("target", link))
	permissions := info["permissions"].(map[string]any)
	if info["path"] != "/Specs/Plan año 2026.md" || info["etag"] == "" || info["file_id"] == "" ||
		info["size_bytes"].(float64) != 18 || permissions["can_write"] != true || permissions["can_delete"] != true {
		t.Errorf("info = %v", info)
	}
	if resolved := h.call("nc_resolve_link", args("url", link)); resolved["path"] != "/Specs/Plan año 2026.md" {
		t.Errorf("resolved = %v", resolved)
	}

	read := h.call("nc_read_file", args("target", link, "max_chars", 7))
	if read["content"] != "línea 1" || read["next_offset"].(float64) != 7 {
		t.Errorf("read = %v", read)
	}
	lines := h.call("nc_read_lines", args("target", "/Specs/Plan año 2026.md"))
	if lines["content"] != "     1| línea 1\n     2| línea 2" {
		t.Errorf("lines = %v", lines)
	}

	h.fails("nc_read_file", args("target", "/missing.md"), "Not found")
	h.fails("nc_list_files", args("path", "/../"), "'..'")
}

func TestRootHidesTheRestOfTheAccount(t *testing.T) {
	outside := newHarness(t, nil)
	outside.put("/only-outside-marker.md", "secret")
	link := outside.call("nc_get_file_info", args("target", "/only-outside-marker.md"))["url"].(string)

	h := newHarness(t, nil)
	if found := h.call("nc_search_files", args("query", "only-outside-marker")); found["count"].(float64) != 0 {
		t.Errorf("search left the root: %v", found)
	}
	h.fails("nc_resolve_link", args("url", link), "not found")
	h.fails("nc_read_file", args("target", link), "not found")
}

func TestCreateAndEdit(t *testing.T) {
	h := newHarness(t, writable)
	dav(t, "MKCOL", h.root+"/Work", "", nil)

	folders := h.call("nc_create_folder", args("path", "/Work/2026/Notes"))
	if len(folders["created"].([]any)) != 2 {
		t.Fatalf("folders = %v", folders)
	}
	if again := h.call("nc_create_folder", args("path", "/Work/2026/Notes")); len(again["created"].([]any)) != 0 {
		t.Errorf("repeat = %v", again)
	}
	h.fails("nc_create_folder", args("path", "/Elsewhere/x"), "only be created inside /Work")

	h.call("nc_create_file", args("path", "/Work/2026/Notes/a.md", "content", "one\ntwo\nthree\n"))
	h.fails("nc_create_file", args("path", "/Work/2026/Notes/a.md", "content", "replaced"), "already exists")
	h.fails("nc_create_file", args("path", "/Missing/a.md", "content", "x"), "parent folder")
	if got := h.get("/Work/2026/Notes/a.md"); got != "one\ntwo\nthree\n" {
		t.Fatalf("content = %q", got)
	}

	h.call("nc_edit_file", args("target", "/Work/2026/Notes/a.md", "old_text", "two", "new_text", "TWO"))
	read := h.call("nc_read_lines", args("target", "/Work/2026/Notes/a.md"))
	edited := h.call("nc_edit_lines", args("target", "/Work/2026/Notes/a.md", "file_hash", read["file_hash"],
		"start_line", 4, "end_line", 3, "new_text", "four"))
	if got := h.get("/Work/2026/Notes/a.md"); got != "one\nTWO\nthree\nfour\n" || edited["total_lines"].(float64) != 4 {
		t.Errorf("content = %q, edited = %v", got, edited)
	}
	h.fails("nc_edit_lines", args("target", "/Work/2026/Notes/a.md", "file_hash", read["file_hash"],
		"start_line", 1, "end_line", 1, "new_text", "x"), "has changed since it was read")
}

// A real Nextcloud must honour If-Match and If-None-Match the way the client relies on.
func TestNextcloudHonoursPreconditions(t *testing.T) {
	h := newHarness(t, nil)
	h.put("/c.md", "v1")
	if status, _ := dav(t, http.MethodPut, h.root+"/c.md", "v2", map[string]string{"If-None-Match": "*"}); status != http.StatusPreconditionFailed {
		t.Errorf("If-None-Match on an existing file: HTTP %d, want 412", status)
	}
	if status, _ := dav(t, http.MethodPut, h.root+"/c.md", "v2", map[string]string{"If-Match": `"not-the-etag"`}); status != http.StatusPreconditionFailed {
		t.Errorf("If-Match with a stale ETag: HTTP %d, want 412", status)
	}
	if got := h.get("/c.md"); got != "v1" {
		t.Errorf("content = %q", got)
	}
}
