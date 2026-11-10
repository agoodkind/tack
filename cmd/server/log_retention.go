package main

import (
	"context"
	"errors"
	"fmt"
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

func removeExpiredCommandLogs(ctx context.Context, dir string, maxAgeDays int) error {
	if maxAgeDays <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		wrapped := fmt.Errorf("list command log directory %s: %w", dir, err)
		slog.ErrorContext(ctx, "log.command_files_list_failed", slog.String("dir", dir), slog.String("err", wrapped.Error()))
		return wrapped
	}
	cutoff := clock.Now().Add(-time.Duration(maxAgeDays) * logRetentionDay)
	removed := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !commandLogFilePattern.MatchString(entry.Name()) {
			continue
		}
		deleted, err := removeExpiredCommandLog(ctx, filepath.Join(dir, entry.Name()), entry, cutoff)
		if err != nil {
			return err
		}
		if deleted {
			removed++
		}
	}
	slog.InfoContext(ctx, "log.command_files_removed", slog.String("dir", dir), slog.Int("removed", removed),
		slog.Int("max_age_days", maxAgeDays))
	return nil
}

// A file that another process deleted after the listing counts as already removed.
func removeExpiredCommandLog(ctx context.Context, path string, entry fs.DirEntry, cutoff time.Time) (bool, error) {
	info, err := entry.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		wrapped := fmt.Errorf("stat command log file %s: %w", path, err)
		slog.ErrorContext(ctx, "log.command_file_stat_failed", slog.String("path", path), slog.String("err", wrapped.Error()))
		return false, wrapped
	}
	if !info.ModTime().Before(cutoff) {
		return false, nil
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		wrapped := fmt.Errorf("remove command log file %s: %w", path, err)
		slog.ErrorContext(ctx, "log.command_file_remove_failed", slog.String("path", path), slog.String("err", wrapped.Error()))
		return false, wrapped
	}
	return true, nil
}
