package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/caarlos0/env/v11"
)

// Load parses environment settings and resolves default log file paths.
func Load() (*Config, error) {
	environment, err := secretEnvironment(serviceSecretNames)
	if err != nil {
		return nil, err
	}
	var configuration Config
	if err := env.ParseWithOptions(&configuration, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("parse server environment: %w", err)
		slog.Error("config.load_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if !filepath.IsAbs(configuration.OpsFDBClusterDir) {
		err := fmt.Errorf("TACK_OPS_FDB_CLUSTER_DIR %q is not an absolute path", configuration.OpsFDBClusterDir)
		slog.Error("config.load_failed", slog.String("err", err.Error()))
		return nil, err
	}
	logsDirectory := xdgStatePath("tack", "logs")
	if configuration.LogJSONFile == "" {
		configuration.LogJSONFile = filepath.Join(logsDirectory, "app.jsonl")
	}
	if configuration.LogTextFile == "" {
		configuration.LogTextFile = filepath.Join(logsDirectory, "app.log")
	}
	// Convert the explicit disabling value to the empty path gklog requires.
	if configuration.LogJSONFile == "-" {
		configuration.LogJSONFile = ""
	}
	if configuration.LogTextFile == "-" {
		configuration.LogTextFile = ""
	}
	return &configuration, nil
}

// xdgStatePath resolves a state path with HOME/.local/state as the fallback.
func xdgStatePath(elements ...string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(append([]string{base}, elements...)...)
}
