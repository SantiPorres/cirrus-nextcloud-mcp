package server_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/samples"
)

var (
	readTools  = []string{"nc_get_file_info", "nc_list_files", "nc_read_file", "nc_read_lines", "nc_resolve_link", "nc_search_files"}
	writeTools = []string{"nc_create_file", "nc_edit_file", "nc_edit_lines"}
)

func TestReadOnlyByDefault(t *testing.T) {
	names := newHarness(t, nil).toolNames()
	for _, name := range readTools {
		if !slices.Contains(names, name) {
			t.Errorf("%s should be available", name)
		}
	}
	for _, name := range append([]string{"nc_create_folder"}, writeTools...) {
		if slices.Contains(names, name) {
			t.Errorf("%s must not exist on a read-only server", name)
		}
	}
	// A tool that is not registered cannot be called at all.
	h := newHarness(t, nil)
	h.fake.AddFile("/a.md", []byte("x"))
	if result, err := h.session.CallTool(t.Context(), callParams("nc_edit_file", args("target", "/a.md", "old_text", "x", "new_text", "y"))); err == nil && !result.IsError {
		t.Error("a write tool was callable on a read-only server")
	}
	if h.file("/a.md") != "x" {
		t.Error("the file was modified")
	}
}

func TestWriteToolsNeedTheirSwitches(t *testing.T) {
	names := newHarness(t, writable).toolNames()
	for _, name := range writeTools {
		if !slices.Contains(names, name) {
			t.Errorf("%s should be available with NEXTCLOUD_ALLOW_WRITE", name)
		}
	}
	if slices.Contains(names, "nc_create_folder") {
		t.Error("nc_create_folder needs NEXTCLOUD_FOLDER_WRITE_PREFIX")
	}
	names = newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_TOOL_GROUPS": "diagnostics"}).toolNames()
	if slices.Contains(names, "nc_list_files") {
		t.Error("a disabled group must not register its tools")
	}
}

func TestListSearchInfoAndLinks(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	id := h.fake.AddFile("/Team/Specs/Plan Año.md", []byte("hola"))
	h.fake.AddFile("/Team/zeta.txt", []byte("z"))
	outside := h.fake.AddFile("/Private/plan.md", []byte("secret"))
	link := fmt.Sprintf("%s/f/%d", h.fake.URL, id)

	listing := h.call("nc_list_files", nil)
	if listing["count"].(float64) != 2 || listing["path"] != "/" {
		t.Errorf("listing = %v", listing)
	}
	first := listing["items"].([]any)[0].(map[string]any)
	if first["name"] != "Specs" || first["type"] != "folder" {
		t.Errorf("folders should come first: %v", first)
	}

	found := h.call("nc_search_files", args("query", "plan"))
	if found["count"].(float64) != 1 {
		t.Fatalf("search must not leave the root: %v", found)
	}
	hit := found["results"].([]any)[0].(map[string]any)
	if hit["path"] != "/Specs/Plan Año.md" || hit["url"] != link {
		t.Errorf("hit = %v", hit)
	}
	h.fails("nc_search_files", args("query", "  "), "must not be empty")

	for _, target := range []string{"/Specs/Plan Año.md", link} {
		info := h.call("nc_get_file_info", args("target", target))
		permissions := info["permissions"].(map[string]any)
		if info["path"] != "/Specs/Plan Año.md" || info["size_bytes"].(float64) != 4 || info["etag"] == "" ||
			info["file_id"] != fmt.Sprint(id) || permissions["can_write"] != true || permissions["can_share"] != true {
			t.Errorf("info(%s) = %v", target, info)
		}
	}

	resolved := h.call("nc_resolve_link", args("url", link))
	if resolved["path"] != "/Specs/Plan Año.md" || resolved["type"] != "file" {
		t.Errorf("resolved = %v", resolved)
	}
	h.fails("nc_resolve_link", args("url", fmt.Sprintf("%s/f/%d", h.fake.URL, outside)), "not found")
	h.fails("nc_resolve_link", args("url", h.fake.URL+"/s/AbCdEf123"), "Unsupported link")
	h.fails("nc_resolve_link", args("url", "https://elsewhere.example/f/1"), "must belong to")
	h.fails("nc_read_file", args("target", fmt.Sprintf("%s/f/%d", h.fake.URL, outside)), "not found")
	h.fails("nc_list_files", args("path", "/../Private"), "'..'")
	h.fails("nc_read_file", args("target", "/Specs/../../Private/plan.md"), "'..'")
}

func TestReadFileWithPaging(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.AddFile("/notes.md", []byte("añb\r\ncd\r\nefgh"))
	page := h.call("nc_read_file", args("target", "/notes.md", "max_chars", 5))
	if page["content"] != "añb\nc" || page["total_chars"].(float64) != 11 || page["next_offset"].(float64) != 5 || page["truncated"] != true {
		t.Errorf("first page = %v", page)
	}
	page = h.call("nc_read_file", args("target", "/notes.md", "offset", 5))
	if page["content"] != "d\nefgh" || page["truncated"] != false {
		t.Errorf("second page = %v", page)
	}
	if _, present := page["next_offset"]; present {
		t.Error("the last page has no next_offset")
	}
	h.fails("nc_read_file", args("target", "/notes.md", "offset", 99), "past the end")
	h.fails("nc_read_file", args("target", "/notes.md", "offset", -1), "negative")
	h.fails("nc_read_file", args("target", "/missing.md"), "Not found")
	h.fails("nc_read_file", args("target", "/"), "must point to a file")
}

func TestReadDocuments(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.AddFile("/r.pdf", samples.PDF("Quarterly report"))
	h.fake.AddFile("/d.docx", samples.Docx(`<w:p><w:r><w:t>Word body</w:t></w:r></w:p>`))
	h.fake.AddFile("/g.drawio", []byte(samples.CompressedDrawio(`<mxGraphModel><root><mxCell id="0"/></root></mxGraphModel>`)))
	h.fake.AddFile("/photo.jpg", []byte{0xff, 0xd8, 0x00})
	for target, want := range map[string]string{"/r.pdf": "Quarterly report", "/d.docx": "Word body", "/g.drawio": `<mxCell id="0" />`} {
		if content := h.call("nc_read_file", args("target", target))["content"].(string); !strings.Contains(content, want) {
			t.Errorf("%s: %q does not contain %q", target, content, want)
		}
	}
	h.fails("nc_read_file", args("target", "/photo.jpg"), "Unsupported file type .jpg")
}

func TestReadLines(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.AddFile("/l.txt", []byte("one\ntwo\nthree\nfour\n"))
	page := h.call("nc_read_lines", args("target", "/l.txt", "max_lines", 2))
	if page["content"] != "     1| one\n     2| two" || page["total_lines"].(float64) != 4 || page["next_start_line"].(float64) != 3 {
		t.Errorf("page = %v", page)
	}
	page = h.call("nc_read_lines", args("target", "/l.txt", "start_line", 3))
	if page["content"] != "     3| three\n     4| four" || page["end_line"].(float64) != 4 {
		t.Errorf("page = %v", page)
	}
	if _, present := page["next_start_line"]; present {
		t.Error("the last page has no next_start_line")
	}
	h.fails("nc_read_lines", args("target", "/l.txt", "start_line", 9), "past the end")
}

func TestCreateFile(t *testing.T) {
	h := newHarness(t, writable)
	h.fake.AddDir("/Specs")
	created := h.call("nc_create_file", args("path", "/Specs/new.md", "content", "# Hi\n"))
	if created["status"] != "created" || created["path"] != "/Specs/new.md" || h.file("/Specs/new.md") != "# Hi\n" {
		t.Errorf("created = %v", created)
	}
	h.fails("nc_create_file", args("path", "/Specs/new.md", "content", "other"), "already exists")
	if h.file("/Specs/new.md") != "# Hi\n" {
		t.Error("an existing file was replaced")
	}
	h.fails("nc_create_file", args("path", "/Specs/run.sh", "content", "x"), "cannot be created")
	h.fails("nc_create_file", args("path", "/Specs/bad.json", "content", "{"), "not be valid JSON")
	h.fails("nc_create_file", args("path", "/Nope/a.md", "content", "x"), "parent folder")
	h.fails("nc_create_file", args("path", "/../a.md", "content", "x"), "'..'")
	h.fails("nc_create_file", args("path", h.fake.URL+"/f/1", "content", "x"), "not a link")
	if h.fake.Exists("/Specs/run.sh") || h.fake.Exists("/Specs/bad.json") {
		t.Error("a refused file was written")
	}
	h.assertNoBlindOverwrite()
}

func TestCreateFileRespectsSizeLimit(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_MAX_TEXT_BYTES": "10"})
	h.fails("nc_create_file", args("path", "/big.md", "content", "12345678901"), "too large")
}

func TestEditFile(t *testing.T) {
	h := newHarness(t, writable)
	id := h.fake.AddFile("/doc.md", []byte("alpha\r\nbeta  \r\ngamma\r\n"))

	edited := h.call("nc_edit_file", args("target", "/doc.md", "old_text", "alpha", "new_text", "ALPHA"))
	if edited["replacements"].(float64) != 1 || edited["matched"] != "exact" {
		t.Errorf("edited = %v", edited)
	}
	if got := h.file("/doc.md"); got != "ALPHA\r\nbeta  \r\ngamma\r\n" {
		t.Errorf("CRLF endings must be kept: %q", got)
	}
	// By link, across lines, tolerating the trailing spaces after "beta".
	edited = h.call("nc_edit_file", args("target", fmt.Sprintf("%s/f/%d", h.fake.URL, id), "old_text", "beta\ngamma", "new_text", "B\nG"))
	if edited["matched"] != "ignoring trailing whitespace" || h.file("/doc.md") != "ALPHA\r\nB\r\nG\r\n" {
		t.Errorf("edited = %v, file = %q", edited, h.file("/doc.md"))
	}

	h.fails("nc_edit_file", args("target", "/doc.md", "old_text", "zzz", "new_text", "y"), "was not found")
	h.fails("nc_edit_file", args("target", "/doc.md", "old_text", "", "new_text", "y"), "must not be empty")
	h.fails("nc_edit_file", args("target", "/doc.md", "old_text", "a", "new_text", "a"), "identical")
	h.fake.AddFile("/twice.md", []byte("x x"))
	h.fails("nc_edit_file", args("target", "/twice.md", "old_text", "x", "new_text", "y"), "appears 2 times")
	h.call("nc_edit_file", args("target", "/twice.md", "old_text", "x", "new_text", "y", "replace_all", true))
	if h.file("/twice.md") != "y y" {
		t.Errorf("replace_all: %q", h.file("/twice.md"))
	}

	h.fake.AddFile("/data.json", []byte(`{"a": 1}`))
	h.fails("nc_edit_file", args("target", "/data.json", "old_text", "}", "new_text", ""), "not be valid JSON")
	if h.file("/data.json") != `{"a": 1}` {
		t.Error("an invalid result was saved")
	}
	h.fake.AddFile("/script.sh", []byte("echo"))
	h.fails("nc_edit_file", args("target", "/script.sh", "old_text", "echo", "new_text", "rm"), "cannot be created or modified")
	h.fake.AddFile("/bin.txt", []byte{0xff, 0xfe, 0x00})
	h.fails("nc_edit_file", args("target", "/bin.txt", "old_text", "a", "new_text", "b"), "not valid UTF-8")
	h.assertNoBlindOverwrite()
}

func TestEditFailsWhenTheFileChangedInBetween(t *testing.T) {
	h := newHarness(t, writable)
	h.fake.AddFile("/doc.md", []byte("alpha beta"))
	h.fake.BeforeNextPut(func() { h.fake.AddFile("/doc.md", []byte("alpha beta, edited by someone else")) })

	h.fails("nc_edit_file", args("target", "/doc.md", "old_text", "alpha", "new_text", "ALPHA"), "modified by someone else")
	if got := h.file("/doc.md"); got != "alpha beta, edited by someone else" {
		t.Errorf("the other person's change was lost: %q", got)
	}
	var sawIfMatch bool
	for _, request := range h.fake.Requests() {
		if request.Method == "PUT" {
			sawIfMatch = request.Header.Get("If-Match") != ""
		}
	}
	if !sawIfMatch {
		t.Error("the PUT did not carry If-Match")
	}
	h.assertNoBlindOverwrite()
}

func TestEditLines(t *testing.T) {
	h := newHarness(t, writable)
	h.fake.AddFile("/l.md", []byte("one\ntwo\nthree\n"))
	hash := h.call("nc_read_lines", args("target", "/l.md"))["file_hash"].(string)

	edited := h.call("nc_edit_lines", args("target", "/l.md", "file_hash", hash, "start_line", 2, "end_line", 2, "new_text", "TWO\n2b"))
	if h.file("/l.md") != "one\nTWO\n2b\nthree\n" || edited["total_lines"].(float64) != 4 || edited["lines_inserted"].(float64) != 2 {
		t.Errorf("edited = %v, file = %q", edited, h.file("/l.md"))
	}
	if !strings.Contains(edited["preview"].(string), "     2| TWO") {
		t.Errorf("preview = %v", edited["preview"])
	}

	// The old hash is now stale; the hash returned by the edit works.
	h.fails("nc_edit_lines", args("target", "/l.md", "file_hash", hash, "start_line", 1, "end_line", 1, "new_text", "x"), "has changed since it was read")
	h.call("nc_edit_lines", args("target", "/l.md", "file_hash", edited["file_hash"], "start_line", 5, "end_line", 4, "new_text", "four"))
	if h.file("/l.md") != "one\nTWO\n2b\nthree\nfour\n" {
		t.Errorf("append: %q", h.file("/l.md"))
	}
	current := h.call("nc_read_lines", args("target", "/l.md"))["file_hash"]
	h.fails("nc_edit_lines", args("target", "/l.md", "file_hash", current, "start_line", 9, "end_line", 9), "start_line must be between")
	h.assertNoBlindOverwrite()
}

func TestEditCompressedDrawioByLines(t *testing.T) {
	h := newHarness(t, writable)
	h.fake.AddFile("/g.drawio", []byte(samples.CompressedDrawio(`<mxGraphModel><root><mxCell id="0" value="old"/></root></mxGraphModel>`)))
	read := h.call("nc_read_lines", args("target", "/g.drawio"))
	if !strings.Contains(read["content"].(string), `     5|     <mxCell id="0" value="old" />`) {
		t.Fatalf("content = %v", read["content"])
	}
	h.call("nc_edit_lines", args("target", "/g.drawio", "file_hash", read["file_hash"], "start_line", 5, "end_line", 5,
		"new_text", `    <mxCell id="0" value="new" />`))
	if saved := h.file("/g.drawio"); !strings.Contains(saved, `value="new"`) || !strings.HasPrefix(saved, "<mxfile") {
		t.Errorf("saved = %q", saved)
	}
	h.fails("nc_edit_file", args("target", "/g.drawio", "old_text", "</mxfile>", "new_text", ""), "not be valid DRAWIO")
}

func TestCreateFolder(t *testing.T) {
	h := newHarness(t, map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true", "NEXTCLOUD_FOLDER_WRITE_PREFIX": "/Projects/Meetings"})
	h.fake.AddDir("/Projects/Meetings")
	h.fake.AddFile("/Projects/Meetings/file.txt", []byte("x"))

	result := h.call("nc_create_folder", args("path", "/Projects/Meetings/2026/Review"))
	if fmt.Sprint(result["created"]) != "[/Projects/Meetings/2026 /Projects/Meetings/2026/Review]" || !h.fake.Exists("/Projects/Meetings/2026/Review") {
		t.Errorf("result = %v", result)
	}
	result = h.call("nc_create_folder", args("path", "Projects/Meetings/2026/Review/"))
	if fmt.Sprint(result["created"]) != "[]" || len(result["already_existed"].([]any)) != 2 {
		t.Errorf("repeat = %v", result)
	}
	for _, outside := range []string{"/Projects/Other", "/Projects/Meetings", "/Projects/MeetingsX/a", "/", "/Elsewhere/Projects/Meetings/a"} {
		h.fails("nc_create_folder", args("path", outside), "only be created inside /Projects/Meetings")
	}
	h.fails("nc_create_folder", args("path", "/Projects/Meetings/../Other"), "'..'")
	h.fails("nc_create_folder", args("path", "/Projects/Meetings/file.txt/sub"), "is a file")
	if h.fake.Exists("/Projects/Other") {
		t.Error("a folder was created outside the prefix")
	}
}
