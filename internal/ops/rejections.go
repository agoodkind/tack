package ops

import (
	"errors"
	"fmt"
	"strconv"
)

var (
	errYBTabletMappingMissing  = errors.New("import_snapshot produced no tablet mapping")
	errYBBackupSettingsMissing = errors.New(
		"yb-snapshot-export: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required",
	)
	errYBMigratorPasswordMissing = errors.New(
		"yb-snapshot-export: TACK_MIGRATOR_PASSWORD is required",
	)
)

type fdbRestoreExitError struct {
	exitCode int
	progress string
	logTail  string
}

func (err *fdbRestoreExitError) Error() string {
	return fmt.Sprintf("fdbrestore exited %d after %s: %s", err.exitCode, err.progress, err.logTail)
}

type ybSnapshotImportExitError struct {
	exitCode int
	stderr   string
}

func (err *ybSnapshotImportExitError) Error() string {
	return fmt.Sprintf("import_snapshot exited %d: %s", err.exitCode, err.stderr)
}

type ybScratchExitedError struct {
	step     string
	progress string
}

func (err *ybScratchExitedError) Error() string {
	return err.step + ": the scratch yugabyted container is not running: " + err.progress
}

type ybScratchFailureError struct {
	step     string
	failure  string
	progress string
}

func (err *ybScratchFailureError) Error() string {
	return err.step + ": " + err.failure + ": " + err.progress
}

type retentionPrefixError struct{ prefix string }

func (err *retentionPrefixError) Error() string {
	return "retention refuses object outside ledger prefix " + err.prefix
}

type fdbBackupMarkerError struct{ marker string }

func (err *fdbBackupMarkerError) Error() string {
	return "FoundationDB backup marker " + strconv.Quote(
		err.marker,
	) + " is not a timestamp run identifier"
}

type fdbActiveBackupMarkerError struct{ name string }

func (err *fdbActiveBackupMarkerError) Error() string {
	return "active FoundationDB backup " + strconv.Quote(
		err.name,
	) + " does not have a discovered marker"
}
