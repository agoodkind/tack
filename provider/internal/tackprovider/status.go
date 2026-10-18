package tackprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// guardedVersion is the minimum applied version required for a target at or
// below this version.
const guardedVersion = 19

// migration represents one embedded migration file in the status document from
// migrate --output json.
type migration struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
}

// schemaStatus represents the result member of the status document.
type schemaStatus struct {
	MigrationTablePresent bool        `json:"migration_table_present"`
	AppliedVersion        int64       `json:"applied_version"`
	Available             []migration `json:"available"`
	ImageCommit           string      `json:"image_commit"`
}

type statusEnvelope struct {
	Result *schemaStatus `json:"result"`
}

// decodeStatus decodes stdout from migrate --output json and requires exactly
// one JSON document.
func decodeStatus(ctx context.Context, stdout []byte) (schemaStatus, error) {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	var envelope statusEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		slog.ErrorContext(ctx, "schema.decode_failed", slog.Int("stdout_bytes", len(stdout)), slog.String("err", err.Error()))
		return schemaStatus{}, fmt.Errorf("decode the status document from %d bytes of stdout: %w", len(stdout), err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return schemaStatus{}, fmt.Errorf("stdout contains additional data after the status document (%d bytes total)", len(stdout))
	}
	if envelope.Result == nil {
		return schemaStatus{}, errors.New("the status document does not contain a result member")
	}
	return *envelope.Result, nil
}

// highest returns the highest available migration version, or 0 if the image
// does not embed any migrations.
func (status schemaStatus) highest() int64 {
	var highest int64
	for _, available := range status.Available {
		highest = max(highest, available.Version)
	}
	return highest
}

// pendingThrough returns the available migrations above the applied version up
// to and including target, appending them in the order of status.Available
// without sorting them.
func (status schemaStatus) pendingThrough(target int64) []migration {
	pending := make([]migration, 0, len(status.Available))
	for _, available := range status.Available {
		if available.Version > status.AppliedVersion && available.Version <= target {
			pending = append(pending, available)
		}
	}
	return pending
}

// targetProblem returns the reason target is invalid for status, or an empty
// string if target is valid.
func (status schemaStatus) targetProblem(target int64) string {
	applied := status.AppliedVersion
	highest := status.highest()
	if target < applied {
		return fmt.Sprintf("The configured version %d is below the applied migration version %d. "+
			"The resource does not revert migrations.", target, applied)
	}
	if target > highest {
		return fmt.Sprintf("The configured version %d exceeds the highest available migration version %d "+
			"in image commit %s.", target, highest, status.ImageCommit)
	}
	if applied < guardedVersion && target <= guardedVersion {
		return fmt.Sprintf("The configured version %d is at or below version %d. The applied migration "+
			"version is %d. A target at or below version %d requires an applied version of at least %d.",
			target, guardedVersion, applied, guardedVersion, guardedVersion)
	}
	return ""
}

// pendingText formats migrations for a plan warning, with one migration per
// line.
func pendingText(pending []migration) string {
	lines := make([]string, 0, len(pending))
	for _, pendingMigration := range pending {
		lines = append(lines, fmt.Sprintf("- version %d: %s (sha256 %s)",
			pendingMigration.Version, pendingMigration.Name, pendingMigration.SHA256))
	}
	return strings.Join(lines, "\n")
}
