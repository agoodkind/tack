package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"goodkind.io/tack/internal/clock"
)

// commandLogFilePattern matches the files that processLogFiles names and the backups that
// lumberjack makes of them: the command path, the request ID, an optional rotation time, the
// .jsonl or .log extension, and an optional .gz suffix.
var commandLogFilePattern = regexp.MustCompile(
	`^[a-z0-9-]+-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}` +
		`(-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}\.[0-9]{3})?\.(jsonl|log)(\.gz)?$`)

const logRetentionDay = 24 * time.Hour

// removeExpiredCommandLogs deletes the command log files in dir that were last modified more
// than maxAgeDays days ago, and logs how many it deleted. It leaves files with other names in
// place. A maxAgeDays of zero or less keeps every file, as lumberjack does for MaxAge 0.
func removeExpiredCommandLogs(ctx context.Context, dir string, maxAgeDays int) {
	if maxAgeDays <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "log.command_files_list_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return
	}
	cutoff := clock.Now().Add(-time.Duration(maxAgeDays) * logRetentionDay)
	removed := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !commandLogFilePattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			slog.ErrorContext(ctx, "log.command_file_stat_failed", slog.String("path", path), slog.String("err", err.Error()))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			slog.ErrorContext(ctx, "log.command_file_remove_failed", slog.String("path", path), slog.String("err", err.Error()))
			continue
		}
		removed++
	}
	slog.InfoContext(ctx, "log.command_files_removed", slog.String("dir", dir), slog.Int("removed", removed),
		slog.Int("max_age_days", maxAgeDays))
}
