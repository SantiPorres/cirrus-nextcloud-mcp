package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/server"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

// harness is an MCP client connected in memory to a server that talks to a fake Nextcloud.
type harness struct {
	t       *testing.T
	fake    *fakedav.Server
	session *mcp.ClientSession
}

// writable is the environment of a server with writes enabled.
var writable = map[string]string{"NEXTCLOUD_ALLOW_WRITE": "true"}

func newHarness(t *testing.T, extra map[string]string) *harness {
	t.Helper()
	fake := fakedav.New(t)
	env := map[string]string{
		"NEXTCLOUD_URL": fake.URL, "NEXTCLOUD_USER": fakedav.User, "NEXTCLOUD_APP_PASSWORD": fakedav.Password,
		"NEXTCLOUD_ALLOW_INSECURE_HTTP": "true",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	mcpServer, _ := server.New(cfg, nextcloud.New(cfg), server.Build{Version: "test", Commit: "abc123"}, slog.New(slog.DiscardHandler))

	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return &harness{t: t, fake: fake, session: session}
}

func (h *harness) toolNames() []string {
	h.t.Helper()
	result, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func (h *harness) raw(name string, args map[string]any) *mcp.CallToolResult {
	h.t.Helper()
	result, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return result
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

// call runs a tool that must succeed and decodes its JSON result.
func (h *harness) call(name string, args map[string]any) map[string]any {
	h.t.Helper()
	result := h.raw(name, args)
	if result.IsError {
		h.t.Fatalf("%s failed: %s", name, text(result))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text(result)), &out); err != nil {
		h.t.Fatalf("%s returned something that is not JSON: %q", name, text(result))
	}
	return out
}

// fails runs a tool that must fail and returns the error text.
func (h *harness) fails(name string, args map[string]any, wantInMessage string) string {
	h.t.Helper()
	result := h.raw(name, args)
	if !result.IsError {
		h.t.Fatalf("%s should have failed, got: %s", name, text(result))
	}
	message := text(result)
	if !strings.Contains(message, wantInMessage) {
		h.t.Errorf("%s: error %q does not mention %q", name, message, wantInMessage)
	}
	if strings.Contains(message, fakedav.Password) {
		h.t.Errorf("%s: the error leaks the password", name)
	}
	return message
}

func (h *harness) file(path string) string {
	h.t.Helper()
	data, ok := h.fake.File(path)
	if !ok {
		h.t.Fatalf("%s does not exist", path)
	}
	return string(data)
}

// assertNoBlindOverwrite checks the safety invariant after a test.
func (h *harness) assertNoBlindOverwrite() {
	h.t.Helper()
	if n := h.fake.UnconditionalOverwrites(); n != 0 {
		h.t.Errorf("%d PUT(s) replaced a file without If-Match", n)
	}
}

func args(pairs ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1]
	}
	return out
}

func callParams(name string, arguments map[string]any) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: name, Arguments: arguments}
}
