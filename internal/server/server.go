// Package server wires configuration, the Nextcloud client and the tool
// groups into an MCP server, and runs it over stdio or streamable HTTP.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools/diagnostics"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools/files"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools/safeops"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools/sheets"
)

// Name is the server name announced to MCP clients.
const Name = "cirrus-nextcloud-mcp"

// Build describes the running binary.
type Build struct {
	Version string
	Commit  string
}

const instructions = "Tools for one Nextcloud account. Paths are relative to the top folder the server is " +
	"restricted to and start with \"/\". Tools that modify, delete or share are only present when the " +
	"operator enabled them. Existing files are never overwritten blindly: edits fail if the file changed " +
	"since it was read."

// New creates the MCP server with every tool the configuration allows.
func New(cfg *config.Config, client *nextcloud.Client, build Build, logger *slog.Logger) (*mcp.Server, *tools.Registry) {
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: build.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	registry := tools.NewRegistry(server, cfg, client, logger)
	registry.Build = tools.Build{Name: Name, Version: build.Version, Commit: build.Commit, StartedAt: time.Now()}
	files.Register(registry)
	sheets.Register(registry)
	safeops.Register(registry)
	// Last, so that it can report on every other tool.
	diagnostics.Register(registry)
	return server, registry
}

// Run serves until ctx is cancelled, over the transport chosen in cfg.
func Run(ctx context.Context, cfg *config.Config, server *mcp.Server, logger *slog.Logger) error {
	if cfg.Transport == config.TransportStdio {
		return server.Run(ctx, &mcp.StdioTransport{})
	}

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           HTTPHandler(server),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.ListenAndServe() }()
	logger.Info("listening", "addr", cfg.HTTPAddr, "endpoint", "/mcp")

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// HTTPHandler serves the MCP endpoint at /mcp and a liveness probe at /healthz.
//
// The endpoint is stateless (no session ids, nothing shared between requests)
// and does NOT authenticate callers: it is meant to sit behind a reverse
// proxy that does.
func HTTPHandler(server *mcp.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
