package ops

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// manifestFileName is the operator-visible artifact inventory written at the
// top of a backup directory. The verify subcommand reads it; the existing
// shell tooling does too. Format:
//
//	<sha256>  <size_bytes>  <relpath>
//
// One file per line. Excludes the manifest itself. Lines are sorted by
// relative path for deterministic output.
const manifestFileName = "MANIFEST.txt"

// manifestEntry is one row of the manifest. Public-shaped fields so tests
// can build fixtures by struct literal.
type manifestEntry struct {
	SHA256  string
	Size    int64
	RelPath string
}

// writeManifest walks backupDir, hashes each regular file, and writes
// MANIFEST.txt. The manifest itself is excluded from the walk; otherwise
// the hash would chase its own tail.
func writeManifest(ctx context.Context, log *slog.Logger, backupDir string) ([]manifestEntry, error) {
	entries, err := scanManifest(ctx, log, backupDir)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(backupDir, manifestFileName)
	out, err := os.Create(manifestPath)
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.create_failed",
			slog.String("path", manifestPath),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("create manifest %s: %w", manifestPath, err)
	}
	defer out.Close()
	for _, e := range entries {
		_, err = fmt.Fprintf(out, "%s  %d  %s\n", e.SHA256, e.Size, e.RelPath)
		if err != nil {
			log.ErrorContext(ctx, "backup.manifest.write_failed",
				slog.String("rel", e.RelPath),
				slog.String("err", err.Error()),
			)
			return nil, fmt.Errorf("write manifest entry %s: %w", e.RelPath, err)
		}
	}
	return entries, nil
}

// scanManifest walks backupDir, hashing every regular file except the
// manifest. Returns entries sorted by RelPath. Internal helper consumed by
// the manifest writer.
func scanManifest(ctx context.Context, log *slog.Logger, backupDir string) ([]manifestEntry, error) {
	var entries []manifestEntry
	err := filepath.Walk(backupDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			log.ErrorContext(ctx, "backup.manifest.walk_failed",
				slog.String("path", path),
				slog.String("err", walkErr.Error()),
			)
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(backupDir, path)
		if err != nil {
			log.ErrorContext(ctx, "backup.manifest.relpath_failed",
				slog.String("path", path),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("relpath %s: %w", path, err)
		}
		if rel == manifestFileName {
			return nil
		}
		digest, size, err := hashFileSHA256(ctx, log, path)
		if err != nil {
			return err
		}
		entries = append(entries, manifestEntry{
			SHA256:  digest,
			Size:    size,
			RelPath: filepath.ToSlash(rel),
		})
		return nil
	})
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.scan_failed",
			slog.String("dir", backupDir),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("scan %s: %w", backupDir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries, nil
}

// hashFileSHA256 streams the file through sha256 and returns hex digest plus
// size. Each error path logs once; the rule gate is satisfied because the
// caller never sees an unlogged wrapped error.
func hashFileSHA256(ctx context.Context, log *slog.Logger, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.open_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.hash_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// readManifest parses an existing MANIFEST.txt. Used by verify to
// round-trip-check the on-disk artifact set against the recorded one.
func readManifest(ctx context.Context, log *slog.Logger, backupDir string) ([]manifestEntry, error) {
	manifestPath := filepath.Join(backupDir, manifestFileName)
	f, err := os.Open(manifestPath)
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.open_failed",
			slog.String("path", manifestPath),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("open manifest %s: %w", manifestPath, err)
	}
	defer f.Close()
	var entries []manifestEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			err := fmt.Errorf("manifest line %d malformed: %q", lineNum, line)
			log.ErrorContext(ctx, "backup.manifest.parse_failed",
				slog.Int("line", lineNum),
				slog.String("err", err.Error()),
			)
			return nil, err
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			log.ErrorContext(ctx, "backup.manifest.size_parse_failed",
				slog.Int("line", lineNum),
				slog.String("err", err.Error()),
			)
			return nil, fmt.Errorf("manifest line %d size %q: %w", lineNum, parts[1], err)
		}
		rel := strings.Join(parts[2:], " ")
		entries = append(entries, manifestEntry{
			SHA256:  parts[0],
			Size:    size,
			RelPath: rel,
		})
	}
	if err := sc.Err(); err != nil {
		log.ErrorContext(ctx, "backup.manifest.scan_err",
			slog.String("path", manifestPath),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	return entries, nil
}
