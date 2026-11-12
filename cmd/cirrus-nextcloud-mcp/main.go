// Command cirrus-nextcloud-mcp is an MCP server for one Nextcloud account.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/server"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = ""
)

func main() {
	// Logs go to stderr: with the stdio transport, stdout belongs to the protocol.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("%s %s (%s)\n", server.Name, version, resolveCommit())
			return nil
		}
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	client := nextcloud.New(cfg)
	build := server.Build{Version: version, Commit: resolveCommit()}
	mcpServer, registry := server.New(cfg, client, build, logger)

	logger.Info("starting",
		"version", build.Version, "commit", build.Commit, "transport", cfg.Transport,
		"groups", strings.Join(cfg.EnabledGroups(), ","), "tools", len(registry.Active()),
		"write", cfg.AllowWrite, "delete", cfg.AllowDelete, "share", cfg.AllowShare,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, cfg, mcpServer, logger)
}

// resolveCommit prefers the value baked in at build time, then GIT_COMMIT,
// then the VCS revision Go records when building from a checkout.
func resolveCommit() string {
	if commit != "" {
		return commit
	}
	if env := strings.TrimSpace(os.Getenv("GIT_COMMIT")); env != "" {
		return env
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}
	return "unknown"
}
