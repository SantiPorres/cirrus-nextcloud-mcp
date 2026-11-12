package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/server"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/fakedav"
)

func TestStreamableHTTPEndpoint(t *testing.T) {
	fake := fakedav.New(t)
	fake.AddFile("/hello.md", []byte("hi"))
	env := map[string]string{
		"NEXTCLOUD_URL": fake.URL, "NEXTCLOUD_USER": fakedav.User, "NEXTCLOUD_APP_PASSWORD": fakedav.Password,
		"NEXTCLOUD_ALLOW_INSECURE_HTTP": "true",
	}
	cfg, err := config.Load(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	mcpServer, _ := server.New(cfg, nextcloud.New(cfg), server.Build{Version: "test"}, slog.New(slog.DiscardHandler))
	web := httptest.NewServer(server.HTTPHandler(mcpServer))
	defer web.Close()

	health, err := http.Get(web.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Errorf("healthz = %d %q", health.StatusCode, body)
	}

	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: web.URL + "/mcp", DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only on a read-only server", tool.Name)
		}
	}
	if !slices.Contains(names, "nc_read_file") {
		t.Errorf("tools = %v", names)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "nc_read_file", Arguments: map[string]any{"target": "/hello.md"}})
	if err != nil || result.IsError {
		t.Fatalf("call over HTTP: %v %v", err, result)
	}

	// The session is stateless: the server sets no session id and no cookies.
	response, err := http.Post(web.URL+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.Header.Get("Mcp-Session-Id") != "" || len(response.Cookies()) != 0 {
		t.Error("the HTTP endpoint must not keep sessions or set cookies")
	}
}
