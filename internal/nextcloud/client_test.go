package nextcloud_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

func newClient(t *testing.T, baseURL string, extra map[string]string) *nextcloud.Client {
	t.Helper()
	env := map[string]string{
		"NEXTCLOUD_URL": baseURL, "NEXTCLOUD_USER": fakedav.User, "NEXTCLOUD_APP_PASSWORD": fakedav.Password,
		"NEXTCLOUD_ALLOW_INSECURE_HTTP": "true",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return nextcloud.New(cfg)
}

func mustTarget(t *testing.T, c *nextcloud.Client, path string) nextcloud.Target {
	t.Helper()
	target, err := c.PathTarget(path, false)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPathsAreConfinedToRoot(t *testing.T) {
	c := newClient(t, "http://nc.test", map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	for input, want := range map[string]string{
		"": "/Team", "/": "/Team", "a/b": "/Team/a/b", "/a//./b/": "/Team/a/b", `a\b`: "/Team/a/b",
	} {
		if got, err := c.Abs(input); err != nil || got != want {
			t.Errorf("Abs(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"..", "/a/../b", `a\..\b`, "../../etc/passwd", "a/\x00b", "a/b\n"} {
		if _, err := c.Abs(bad); err == nil {
			t.Errorf("Abs(%q) must be refused", bad)
		}
	}
	for abs, want := range map[string]string{"/Team": "/", "/Team/a": "/a", "/Team/a/": "/a"} {
		if got, ok := c.Rel(abs); !ok || got != want {
			t.Errorf("Rel(%q) = %q, %v", abs, got, ok)
		}
	}
	for _, outside := range []string{"", "/Teammate/x", "/Other"} {
		if _, ok := c.Rel(outside); ok {
			t.Errorf("Rel(%q) must be outside the root", outside)
		}
	}
	if _, err := c.PathTarget("/", true); err == nil {
		t.Error("the top folder is not a file")
	}
}

func TestParseFileLink(t *testing.T) {
	c := newClient(t, "http://nc.test/cloud", nil)
	for link, want := range map[string]string{
		"http://nc.test/cloud/f/42":                   "42",
		"http://NC.test/cloud/index.php/f/42/":        "42",
		"http://nc.test/cloud/apps/files/files/7?x=1": "7",
	} {
		if got, err := c.ParseFileLink(link); err != nil || got != want {
			t.Errorf("ParseFileLink(%q) = %q, %v", link, got, err)
		}
	}
	for _, bad := range []string{
		"http://evil.test/cloud/f/42", "http://nc.test/f/42", "http://nc.test/cloud/s/AbCdEf",
		"http://nc.test/cloud/f/abc", "ftp://nc.test/cloud/f/1", "/f/42", "http://nc.test/cloud/f/42/../../x",
	} {
		if _, err := c.ParseFileLink(bad); err == nil {
			t.Errorf("ParseFileLink(%q) must be refused", bad)
		}
	}
}

func TestListStatAndSearchStayInsideRoot(t *testing.T) {
	fake := fakedav.New(t)
	fake.AddFile("/Team/Specs/readme.md", []byte("hello"))
	fake.AddFile("/Team/b notes.txt", []byte("x"))
	fake.AddFile("/Team/A file.txt", []byte("x"))
	secretID := fake.AddFile("/Private/notes-secret.txt", []byte("x"))
	c := newClient(t, fake.URL, map[string]string{"NEXTCLOUD_ROOT": "/Team"})
	ctx := context.Background()

	entries, err := c.List(ctx, mustTarget(t, c, "/"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, "|"); got != "Specs|A file.txt|b notes.txt" {
		t.Errorf("listing = %s", got)
	}
	if !entries[0].IsFolder || entries[1].Path != "/A file.txt" || entries[1].Size != 1 || entries[1].FileID == "" {
		t.Errorf("unexpected entries: %+v", entries)
	}

	entry, err := c.Stat(ctx, mustTarget(t, c, "/Specs/readme.md"))
	if err != nil || entry.Size != 5 || entry.ETag == "" || entry.Permissions != "RGDNVW" || entry.IsFolder {
		t.Errorf("stat = %+v, %v", entry, err)
	}
	if _, err := c.Stat(ctx, mustTarget(t, c, "/missing.md")); err == nil {
		t.Error("stat of a missing file must fail")
	}

	found, err := c.SearchByName(ctx, "NOTES", 10)
	if err != nil || len(found) != 1 || found[0].Path != "/b notes.txt" {
		t.Errorf("search = %+v, %v", found, err)
	}
	if _, err := c.FindByID(ctx, itoa(secretID)); err == nil {
		t.Error("a file outside the root must not be found by id")
	}
	if _, err := c.Resolve(ctx, fake.URL+"/f/"+itoa(secretID), true); err == nil {
		t.Error("a link to a file outside the root must not resolve")
	}
	if _, err := c.Resolve(ctx, fake.URL+"/f/"+entry.FileID, true); err != nil {
		t.Errorf("link to a file inside the root: %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestPutNeverOverwritesBlindly(t *testing.T) {
	fake := fakedav.New(t)
	fake.AddFile("/a.txt", []byte("v1"))
	c := newClient(t, fake.URL, nil)
	ctx := context.Background()
	target := mustTarget(t, c, "/a.txt")

	if _, err := c.Put(ctx, target, []byte("x"), "text/plain", nextcloud.PutCondition{}); err == nil {
		t.Error("a PUT without a condition must be refused before it is sent")
	}
	if _, err := c.Put(ctx, target, []byte("x"), "text/plain", nextcloud.PutCondition{CreateOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("create over an existing file: %v", err)
	}
	if _, err := c.Put(ctx, target, []byte("x"), "text/plain", nextcloud.PutCondition{IfMatch: `"stale"`}); err == nil ||
		!strings.Contains(err.Error(), "modified by someone else") {
		t.Errorf("stale etag: %v", err)
	}
	if data, _ := fake.File("/a.txt"); string(data) != "v1" {
		t.Errorf("file changed to %q", data)
	}

	data, etag, err := c.Download(ctx, target, 1024)
	if err != nil || string(data) != "v1" || etag == "" {
		t.Fatalf("download: %q %q %v", data, etag, err)
	}
	newETag, err := c.Put(ctx, target, []byte("v2"), "text/plain", nextcloud.PutCondition{IfMatch: etag})
	if err != nil || newETag == "" || newETag == etag {
		t.Fatalf("conditional put: %q %v", newETag, err)
	}
	if _, err := c.Put(ctx, mustTarget(t, c, "/missing/b.txt"), []byte("x"), "text/plain", nextcloud.PutCondition{CreateOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "parent folder") {
		t.Errorf("missing parent: %v", err)
	}
	if fake.UnconditionalOverwrites() != 0 {
		t.Error("an unconditional overwrite reached the server")
	}
}

func TestDownloadLimit(t *testing.T) {
	fake := fakedav.New(t)
	fake.AddFile("/big.bin", make([]byte, 2000))
	c := newClient(t, fake.URL, nil)
	if _, _, err := c.Download(context.Background(), mustTarget(t, c, "/big.bin"), 1000); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Errorf("expected a size error, got %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	defer redirecting.Close()

	c := newClient(t, redirecting.URL, nil)
	_, _, err := c.Download(context.Background(), mustTarget(t, c, "/a.txt"), 1024)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("expected a redirect error, got %v", err)
	}
	if reached.Load() != 0 {
		t.Error("the redirect was followed: credentials could leak to another host")
	}
}

func TestErrorsNeverContainThePassword(t *testing.T) {
	// Wrong credentials, an unreachable host and a server error.
	fake := fakedav.New(t)
	wrong := newClient(t, fake.URL, map[string]string{"NEXTCLOUD_APP_PASSWORD": "wrong-" + fakedav.Password})
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()

	for name, c := range map[string]*nextcloud.Client{
		"wrong password": wrong, "server error": newClient(t, failing.URL, nil), "unreachable": newClient(t, dead.URL, nil),
	} {
		_, err := c.List(context.Background(), mustTarget(t, c, "/"))
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if strings.Contains(err.Error(), fakedav.Password) {
			t.Errorf("%s: the error leaks the password: %v", name, err)
		}
	}
	if got := wrong.Scrub("x wrong-" + fakedav.Password + " y"); strings.Contains(got, fakedav.Password) {
		t.Errorf("Scrub left the secret: %s", got)
	}
}

func TestRequestsCarryBasicAuthOnly(t *testing.T) {
	fake := fakedav.New(t)
	c := newClient(t, fake.URL, nil)
	if _, err := c.List(context.Background(), mustTarget(t, c, "/")); err != nil {
		t.Fatal(err)
	}
	for _, request := range fake.Requests() {
		if !strings.HasPrefix(request.Header.Get("Authorization"), "Basic ") {
			t.Error("missing basic auth")
		}
		if request.Header.Get("Cookie") != "" {
			t.Error("the client must not send cookies")
		}
		if strings.Contains(request.Path, fakedav.Password) {
			t.Error("the password must never be in the URL")
		}
	}
}

func TestMkcol(t *testing.T) {
	fake := fakedav.New(t)
	fake.AddFile("/dir/file.txt", []byte("x"))
	c := newClient(t, fake.URL, nil)
	ctx := context.Background()
	if created, err := c.Mkcol(ctx, mustTarget(t, c, "/dir/new")); err != nil || !created {
		t.Errorf("create: %v %v", created, err)
	}
	if created, err := c.Mkcol(ctx, mustTarget(t, c, "/dir/new")); err != nil || created {
		t.Errorf("existing: %v %v", created, err)
	}
	if _, err := c.Mkcol(ctx, mustTarget(t, c, "/dir/file.txt")); err == nil {
		t.Error("a file in the way must be an error")
	}
	if _, err := c.Mkcol(ctx, mustTarget(t, c, "/nope/deeper")); err == nil {
		t.Error("a missing parent must be an error")
	}
}
