//go:build integration

// Package integration runs the MCP tools against a real Nextcloud started
// with docker-compose.test.yml. Run it with `make integration`.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/server"
)

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

var (
	baseURL  = envOr("NC_TEST_URL", "http://127.0.0.1:8089")
	user     = envOr("NC_TEST_USER", "admin")
	password = envOr("NC_TEST_PASSWORD", "integration-test-only")
)

type harness struct {
	t       *testing.T
	session *mcp.ClientSession
	// root is the folder (absolute in the account) this server is restricted to.
	root string
}

// dav sends a raw WebDAV request as the test user, bypassing the MCP server.
func dav(t *testing.T, method, path string, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method,
		baseURL+"/remote.php/dav/files/"+user+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, password)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Nextcloud is not reachable at %s (run `make integration-up`): %v", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// newHarness creates a fresh folder in Nextcloud and a server restricted to it.
func newHarness(t *testing.T, extra map[string]string) *harness {
	t.Helper()
	root := fmt.Sprintf("/mcp-it-%d", time.Now().UnixNano())
	if status, body := dav(t, "MKCOL", root, "", nil); status != http.StatusCreated {
		t.Fatalf("could not create %s: HTTP %d %s", root, status, body)
	}
	t.Cleanup(func() { dav(t, http.MethodDelete, root, "", nil) })

	env := map[string]string{
		"NEXTCLOUD_URL": baseURL, "NEXTCLOUD_USER": user, "NEXTCLOUD_APP_PASSWORD": password,
		"NEXTCLOUD_ALLOW_INSECURE_HTTP": "true", "NEXTCLOUD_ROOT": root,
		"NEXTCLOUD_TOOL_GROUPS": strings.Join(config.AllGroups, ","),
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	mcpServer, _ := server.New(cfg, nextcloud.New(cfg), server.Build{Version: "integration"}, slog.New(slog.DiscardHandler))

	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "integration"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return &harness{t: t, session: session, root: root}
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (h *harness) raw(name string, arguments map[string]any) *mcp.CallToolResult {
	h.t.Helper()
	result, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return result
}

func (h *harness) call(name string, arguments map[string]any) map[string]any {
	h.t.Helper()
	result := h.raw(name, arguments)
	if result.IsError {
		h.t.Fatalf("%s failed: %s", name, text(result))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text(result)), &out); err != nil {
		h.t.Fatalf("%s returned something that is not JSON: %q", name, text(result))
	}
	return out
}

func (h *harness) fails(name string, arguments map[string]any, want string) {
	h.t.Helper()
	result := h.raw(name, arguments)
	if !result.IsError {
		h.t.Fatalf("%s should have failed, got: %s", name, text(result))
	}
	if message := text(result); !strings.Contains(message, want) {
		h.t.Errorf("%s: error %q does not mention %q", name, message, want)
	} else if strings.Contains(message, password) {
		h.t.Errorf("%s: the error leaks the password", name)
	}
}

// put writes a file directly in Nextcloud, relative to the harness root.
func (h *harness) put(path, content string) {
	h.t.Helper()
	if status, body := dav(h.t, http.MethodPut, h.root+path, content, nil); status != http.StatusCreated && status != http.StatusNoContent {
		h.t.Fatalf("PUT %s: HTTP %d %s", path, status, body)
	}
}

// get reads a file directly from Nextcloud, relative to the harness root.
func (h *harness) get(path string) string {
	h.t.Helper()
	status, body := dav(h.t, http.MethodGet, h.root+path, "", nil)
	if status != http.StatusOK {
		h.t.Fatalf("GET %s: HTTP %d", path, status)
	}
	return body
}

func args(pairs ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1]
	}
	return out
}
