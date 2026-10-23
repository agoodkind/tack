package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

type retentionFDBAction struct {
	Name                string              `json:"name"`
	Action              string              `json:"action"`
	Window              fdbRestorableWindow `json:"window"`
	ExpireBeforeVersion int64               `json:"expire_before_version,omitempty"`
}

func planRetentionFDB(ctx context.Context, cfg *config.Config, cutoff time.Time) ([]retentionFDBAction, error) {
	if _, err := fdbRestoreTimestampArg(cutoff.UTC()); err != nil {
		return nil, err
	}
	markers, err := listImmediateObjects(ctx, newBackupS3Client(cfg), cfg.BackupS3BucketMain, fdbBackupMarkerPrefix)
	if err != nil {
		return nil, err
	}
	if len(markers) == 0 {
		return nil, nil
	}
	status, err := retentionFDBCommand(ctx, cfg, []string{"status"})
	if err != nil {
		return nil, err
	}
	activeName, err := retentionFDBActiveName(cfg, status)
	if err != nil {
		return nil, err
	}
	windows := make(map[string]fdbRestorableWindow, len(markers))
	for _, name := range fdbBackupNames(markers) {
		if _, err := time.Parse("20060102T150405Z", name); err != nil {
			return nil, fmt.Errorf("FoundationDB backup marker %q is not a timestamp run identifier", name)
		}
		window, err := retentionFDBWindow(ctx, cfg, name)
		if err != nil {
			return nil, err
		}
		windows[name] = window
	}
	if activeName != "" {
		if _, exists := windows[activeName]; !exists {
			return nil, fmt.Errorf("active FoundationDB backup %q does not have a discovered marker", activeName)
		}
	}
	actions := make([]retentionFDBAction, 0, len(markers))
	for _, name := range fdbBackupNames(markers) {
		window := windows[name]
		if !window.Min.Before(cutoff) {
			continue
		}
		if !window.Max.Before(cutoff) {
			version, err := retentionFDBExpireVersion(ctx, cfg, name, cutoff)
			if err != nil {
				return nil, err
			}
			actions = append(actions, retentionFDBAction{Name: name, Action: "expire", Window: window, ExpireBeforeVersion: version})
			continue
		}
		if name == activeName {
			return nil, fmt.Errorf("active FoundationDB backup %s does not establish restorability at cutoff %s", name, formatFDBTime(cutoff))
		}
		if !retentionFDBHasSurvivor(name, cutoff, windows) {
			return nil, fmt.Errorf("expired FoundationDB backup %s does not have a verified newer retained survivor", name)
		}
		actions = append(actions, retentionFDBAction{Name: name, Action: "delete", Window: window, ExpireBeforeVersion: 0})
	}
	return actions, nil
}

func retentionFDBHasSurvivor(name string, cutoff time.Time, windows map[string]fdbRestorableWindow) bool {
	for otherName, otherWindow := range windows {
		if otherName > name && !otherWindow.Max.Before(cutoff) {
			return true
		}
	}
	return false
}

func retentionFDBActiveName(cfg *config.Config, status string) (string, error) {
	active, err := fdbBackupActiveFromStatus(cfg, status)
	if err != nil {
		wrapped := fmt.Errorf("identify active FoundationDB backup: %w", err)
		slog.Error("backup.retention.fdb.identity_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if !active {
		lower := strings.ToLower(status)
		if !strings.Contains(lower, "completed") && !strings.Contains(lower, "no previous backups") && !strings.Contains(lower, "aborted") {
			return "", fmt.Errorf("FoundationDB status does not establish an active or closed backup identity")
		}
		return "", nil
	}
	backupURL, _ := fdbBackupURLFromStatus(status)
	parsed, err := url.Parse(backupURL)
	if err != nil {
		return "", fmt.Errorf("parse active FoundationDB backup identity")
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if _, err := time.Parse("20060102T150405Z", name); err != nil {
		return "", fmt.Errorf("active FoundationDB backup does not identify a timestamp run")
	}
	return name, nil
}

func retentionFDBWindow(ctx context.Context, cfg *config.Config, name string) (fdbRestorableWindow, error) {
	destination, err := fdbBlobstoreURL(cfg, name)
	if err != nil {
		return fdbRestorableWindow{}, err
	}
	output, err := retentionFDBCommand(ctx, cfg, []string{"describe", "-d", destination, "--version-timestamps"})
	if err != nil {
		return fdbRestorableWindow{}, err
	}
	window, err := fdbRestorableWindowFromDescribe(output)
	if err != nil {
		wrapped := fmt.Errorf("read FoundationDB backup %s window: %w", name, err)
		telemetry.L(ctx).ErrorContext(ctx, "backup.retention.fdb.window_failed", slog.String("name", name), slog.String("err", wrapped.Error()))
		return fdbRestorableWindow{}, wrapped
	}
	return window, nil
}
