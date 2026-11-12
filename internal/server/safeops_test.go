package server_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

var (
	writeDelete = map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ALLOW_DELETE": "true"}
	deleteOnly  = map[string]string{"NEXTCLOUD_ALLOW_DELETE": "true"}
)

func TestSafeOpsPermissionsAreSeparate(t *testing.T) {
	has := func(env map[string]string, name string) bool {
		return slices.Contains(newHarness(t, env).toolNames(), name)
	}
	for _, name := range []string{"nc_trash_list", "nc_versions_list"} {
		if !has(nil, name) {
			t.Errorf("%s is read-only and should always be available", name)
		}
	}
	for _, name := range []string{"nc_move", "nc_copy", "nc_trash_restore", "nc_versions_restore"} {
		if has(nil, name) || has(deleteOnly, name) || !has(writable, name) {
			t.Errorf("%s must depend on NEXTCLOUD_ALLOW_WRITE only", name)
		}
	}
	if has(nil, "nc_delete") || has(writable, "nc_delete") || !has(deleteOnly, "nc_delete") {
		t.Error("nc_delete must depend on NEXTCLOUD_ALLOW_DELETE only")
	}
}

func TestMoveAndCopyNeverOverwrite(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ROOT": "/Team"})
	id := h.fake.AddFile("/Team/a.md", []byte("A"))
	h.fake.AddFile("/Team/b.md", []byte("B"))
	h.fake.AddFile("/Team/Folder/inner.md", []byte("I"))
	h.fake.AddDir("/Team/Archive")
	h.fake.AddFile("/Outside/x.md", []byte("X"))

	moved := h.call("nc_move", args("source", fmt.Sprintf("%s/f/%d", h.fake.URL, id), "destination", "/Archive/a renamed.md"))
	if moved["from"] != "/a.md" || moved["to"] != "/Archive/a renamed.md" || h.fake.Exists("/Team/a.md") || h.file("/Team/Archive/a renamed.md") != "A" {
		t.Errorf("moved = %v", moved)
	}
	h.call("nc_copy", args("source", "/b.md", "destination", "/Archive/b copy.md"))
	if h.file("/Team/b.md") != "B" || h.file("/Team/Archive/b copy.md") != "B" {
		t.Error("copy did not keep both files")
	}
	h.call("nc_move", args("source", "/Folder", "destination", "/Archive/Folder"))
	if h.file("/Team/Archive/Folder/inner.md") != "I" || h.fake.Exists("/Team/Folder") {
		t.Error("folder move")
	}

	for _, tool := range []string{"nc_move", "nc_copy"} {
		h.fails(tool, args("source", "/b.md", "destination", "/Archive/b copy.md"), "already exists")
		h.fails(tool, args("source", "/b.md", "destination", "/Missing/b.md"), "parent folder")
		h.fails(tool, args("source", "/nope.md", "destination", "/x.md"), "was not found")
		h.fails(tool, args("source", "/b.md", "destination", "/b.md"), "are the same")
		h.fails(tool, args("source", "/Archive", "destination", "/Archive/Sub/Archive"), "into itself")
		h.fails(tool, args("source", "/", "destination", "/x"), "top folder")
		h.fails(tool, args("source", "/b.md", "destination", "/"), "top folder")
		h.fails(tool, args("source", "/b.md", "destination", "/../Outside/b.md"), "'..'")
		h.fails(tool, args("source", "/../Outside/x.md", "destination", "/x.md"), "'..'")
		h.fails(tool, args("source", "/b.md", "destination", h.fake.URL+"/f/1"), "not a link")
	}
	if h.file("/Team/Archive/b copy.md") != "B" || h.file("/Outside/x.md") != "X" || h.fake.Exists("/Outside/b.md") {
		t.Error("a refused operation changed something")
	}
	for _, request := range h.fake.Requests() {
		if (request.Method == "MOVE" || request.Method == "COPY") && request.Header.Get("Overwrite") != "F" {
			t.Errorf("%s without Overwrite: F", request.Method)
		}
	}
	h.assertNoBlindOverwrite()
}

func TestDeleteGoesToTrashAndCanBeRestored(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ALLOW_DELETE": "true", "NEXTCLOUD_ROOT": "/Team"})
	h.fake.AddFile("/Team/Docs/report.md", []byte("report"))
	h.fake.AddFile("/Team/Folder/a.md", []byte("a"))
	h.fake.AddFile("/Private/secret.md", []byte("s"))

	deleted := h.call("nc_delete", args("target", "/Docs/report.md"))
	trashID, _ := deleted["trash_id"].(string)
	if deleted["status"] != "moved to trash" || trashID == "" || h.fake.Exists("/Team/Docs/report.md") {
		t.Fatalf("deleted = %v", deleted)
	}
	h.call("nc_delete", args("target", "/Folder"))
	h.fails("nc_delete", args("target", "/"), "top folder")
	h.fails("nc_delete", args("target", "/gone.md"), "Not found")
	h.fails("nc_delete", args("target", "/../Private/secret.md"), "'..'")

	listing := h.call("nc_trash_list", nil)
	if listing["count"].(float64) != 2 {
		t.Fatalf("trash = %v", listing)
	}
	var paths []string
	for _, item := range listing["items"].([]any) {
		paths = append(paths, item.(map[string]any)["original_path"].(string))
	}
	slices.Sort(paths)
	if fmt.Sprint(paths) != "[/Docs/report.md /Folder]" {
		t.Errorf("original paths = %v", paths)
	}

	restored := h.call("nc_trash_restore", args("trash_id", trashID))
	if restored["status"] != "restored" || h.file("/Team/Docs/report.md") != "report" {
		t.Errorf("restored = %v", restored)
	}
	h.fails("nc_trash_restore", args("trash_id", trashID), "no item")
	h.fails("nc_trash_restore", args("trash_id", "../x"), "not valid")
	if h.fake.PermanentDeletes() != 0 {
		t.Error("something was deleted permanently")
	}
}

func TestTrashOutsideTheRootIsInvisible(t *testing.T) {
	// One account, two servers: an unrestricted one deletes, a restricted one must not see it.
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_ROOT": "/Team"})
	h.fake.AddFile("/Private/secret.md", []byte("s"))
	h.fake.AddFile("/Team/mine.md", []byte("m"))
	h.fake.DeleteToTrash("/Private/secret.md")
	h.fake.DeleteToTrash("/Team/mine.md")

	listing := h.call("nc_trash_list", nil)
	items := listing["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["original_path"] != "/mine.md" {
		t.Fatalf("trash = %v", listing)
	}
	h.fails("nc_trash_restore", args("trash_id", h.fake.TrashIDs()[0]), "no item")
	if h.fake.Exists("/Private/secret.md") {
		t.Error("an item outside the root was restored")
	}
}

func TestDeleteIsRefusedWithoutATrashBin(t *testing.T) {
	h := newHarness(t, writeDelete)
	h.fake.AddFile("/keep.md", []byte("keep"))
	h.fake.DisableTrash()
	h.fails("nc_delete", args("target", "/keep.md"), "would be permanent")
	if h.file("/keep.md") != "keep" || h.fake.PermanentDeletes() != 0 {
		t.Error("the file was deleted although there is no trash bin")
	}
	for _, request := range h.fake.Requests() {
		if request.Method == "DELETE" {
			t.Error("a DELETE was sent although there is no trash bin")
		}
	}
}

func TestVersions(t *testing.T) {
	h := newHarness(t, writable)
	id := h.fake.AddFile("/doc.md", []byte("version one"))
	h.fake.AddFile("/doc.md", []byte("version two!"))
	h.fake.AddDir("/Folder")

	listed := h.call("nc_versions_list", args("target", fmt.Sprintf("%s/f/%d", h.fake.URL, id)))
	versions := listed["versions"].([]any)
	etag, _ := listed["current_etag"].(string)
	if listed["path"] != "/doc.md" || len(versions) != 1 || etag == "" {
		t.Fatalf("listed = %v", listed)
	}
	version := versions[0].(map[string]any)
	versionID := version["version_id"].(string)
	if version["size_bytes"].(float64) != 11 || version["author"] != "alice" {
		t.Errorf("version = %v", version)
	}
	h.fails("nc_versions_list", args("target", "/Folder"), "only files have versions")

	h.fails("nc_versions_restore", args("target", "/doc.md", "version_id", versionID, "etag", `"stale"`), "modified since")
	h.fails("nc_versions_restore", args("target", "/doc.md", "version_id", versionID, "etag", ""), "etag is required")
	h.fails("nc_versions_restore", args("target", "/doc.md", "version_id", "../../x", "etag", etag), "not a valid version id")
	h.fails("nc_versions_restore", args("target", "/doc.md", "version_id", "123", "etag", etag), "does not exist")
	if h.file("/doc.md") != "version two!" {
		t.Error("a refused restore changed the file")
	}

	// The etag is accepted with or without quotes.
	restored := h.call("nc_versions_restore", args("target", "/doc.md", "version_id", versionID, "etag", strings.Trim(etag, `"`)))
	if restored["status"] != "restored" || h.file("/doc.md") != "version one" || restored["etag"] == etag {
		t.Errorf("restored = %v", restored)
	}
	// The replaced content is kept as a version: the restore can be undone.
	if again := h.call("nc_versions_list", args("target", "/doc.md")); again["count"].(float64) != 2 {
		t.Errorf("versions after restore = %v", again)
	}
}
