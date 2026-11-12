//go:build integration

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

var allPermissions = map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ALLOW_DELETE": "true"}

func TestMoveCopyNeverOverwrite(t *testing.T) {
	h := newHarness(t, allPermissions)
	dav(t, "MKCOL", h.root+"/Archive", "", nil)
	h.put("/a.md", "A")
	h.put("/b.md", "B")

	h.call("nc_move", args("source", "/a.md", "destination", "/Archive/a renamed.md"))
	h.call("nc_copy", args("source", "/b.md", "destination", "/Archive/b copy.md"))
	if h.get("/Archive/a renamed.md") != "A" || h.get("/Archive/b copy.md") != "B" || h.get("/b.md") != "B" {
		t.Error("move/copy results are wrong")
	}
	h.fails("nc_read_file", args("target", "/a.md"), "Not found")

	h.fails("nc_copy", args("source", "/b.md", "destination", "/Archive/b copy.md"), "already exists")
	h.fails("nc_move", args("source", "/b.md", "destination", "/Archive/b copy.md"), "already exists")
	h.fails("nc_move", args("source", "/b.md", "destination", "/Missing/b.md"), "parent folder")
	h.fails("nc_move", args("source", "/nope.md", "destination", "/x.md"), "was not found")
	if h.get("/Archive/b copy.md") != "B" || h.get("/b.md") != "B" {
		t.Error("a refused operation changed a file")
	}
}

func TestDeleteRestoreFromTrash(t *testing.T) {
	h := newHarness(t, allPermissions)
	dav(t, "MKCOL", h.root+"/Docs", "", nil)
	h.put("/Docs/report.md", "report")

	deleted := h.call("nc_delete", args("target", "/Docs/report.md"))
	trashID, _ := deleted["trash_id"].(string)
	if deleted["status"] != "moved to trash" || trashID == "" {
		t.Fatalf("deleted = %v", deleted)
	}
	h.fails("nc_read_file", args("target", "/Docs/report.md"), "Not found")

	listing := h.call("nc_trash_list", nil)
	items := listing["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("only items from this server's root should be listed: %v", listing)
	}
	item := items[0].(map[string]any)
	if item["trash_id"] != trashID || item["original_path"] != "/Docs/report.md" || item["deleted_at"] == nil {
		t.Errorf("item = %v", item)
	}

	// Another server restricted to another folder cannot see or restore it.
	other := newHarness(t, allPermissions)
	if count := other.call("nc_trash_list", nil)["count"].(float64); count != 0 {
		t.Errorf("the trash of another root is visible: %v items", count)
	}
	other.fails("nc_trash_restore", args("trash_id", trashID), "no item")

	h.call("nc_trash_restore", args("trash_id", trashID))
	if h.get("/Docs/report.md") != "report" {
		t.Error("the file was not restored")
	}
}

func TestVersionsListAndRestore(t *testing.T) {
	h := newHarness(t, allPermissions)
	// Nextcloud only keeps the old content as a version when the file is not
	// brand new, so the first upload is dated one day ago.
	old := strconv.FormatInt(time.Now().Add(-24*time.Hour).Unix(), 10)
	if status, body := dav(t, http.MethodPut, h.root+"/doc.md", "first version\n", map[string]string{"X-OC-Mtime": old}); status != http.StatusCreated {
		t.Fatalf("PUT: HTTP %d %s", status, body)
	}
	h.call("nc_edit_file", args("target", "/doc.md", "old_text", "first", "new_text", "second"))

	listed := h.call("nc_versions_list", args("target", "/doc.md"))
	versions := listed["versions"].([]any)
	etag, _ := listed["current_etag"].(string)
	if len(versions) == 0 || etag == "" {
		t.Fatalf("listed = %v", listed)
	}
	// Find the version holding the first content: its size is that of "first version\n".
	var versionID string
	for _, raw := range versions {
		version := raw.(map[string]any)
		if size, ok := version["size_bytes"].(float64); ok && int(size) == len("first version\n") {
			versionID = version["version_id"].(string)
		}
	}
	if versionID == "" {
		t.Fatalf("no version with the first content in %v", versions)
	}

	h.fails("nc_versions_restore", args("target", "/doc.md", "version_id", versionID, "etag", `"stale-etag"`), "modified since")
	if h.get("/doc.md") != "second version\n" {
		t.Error("a refused restore changed the file")
	}
	restored := h.call("nc_versions_restore", args("target", "/doc.md", "version_id", versionID, "etag", etag))
	if restored["status"] != "restored" || h.get("/doc.md") != "first version\n" {
		t.Errorf("restored = %v, content = %q", restored, h.get("/doc.md"))
	}
}

func TestServerInfoAgainstNextcloud(t *testing.T) {
	h := newHarness(t, nil)
	info := h.call("get_server_info", args("check_connection", true))
	nextcloud := info["nextcloud"].(map[string]any)
	if nextcloud["reachable"] != true || nextcloud["trash_enabled"] != true || !strings.Contains(nextcloud["version"].(string), ".") {
		t.Errorf("nextcloud = %v", nextcloud)
	}
	if strings.Contains(text(h.raw("get_server_info", nil)), password) {
		t.Error("get_server_info leaks the password")
	}
}
