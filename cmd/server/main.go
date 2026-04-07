package main

import (
	"context"
	"log/slog"
	"os"

	mcpadapter "github.com/agoodkind/tack/internal/adapters/mcp"
	"github.com/agoodkind/tack/internal/adapters/postgres"
	"github.com/agoodkind/tack/internal/config"
	"github.com/agoodkind/tack/internal/service"
	"github.com/agoodkind/tack/migrations"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	setupLogger(cfg)

	ctx := context.Background()

	// Storage
	pool, err := postgres.New(ctx, cfg.DatabaseURL, migrations.FS)
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories
	issueRepo := postgres.NewIssueRepo(pool)
	projectRepo := postgres.NewProjectRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	_ = tokenRepo // used by REST middleware in phase 2

	// Services
	issueSvc := service.NewIssueService(issueRepo, projectRepo)

	// MCP — stdio transport: Claude Code spawns this process and pipes JSON-RPC.
	// Run as: go run ./cmd/server mcp
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		mcpServer := mcpadapter.New(issueSvc)
		slog.Info("starting MCP server (stdio)")
		if err := server.ServeStdio(mcpServer); err != nil {
			slog.Error("mcp stdio", "err", err)
			os.Exit(1)
		}
		return
	}

	// Phase 2: HTTP server (REST + MCP SSE) starts here.
	slog.Info("HTTP server not yet implemented; run with 'mcp' subcommand")
}

func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	var h slog.Handler
	if cfg.Env == "production" {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(h))
}
