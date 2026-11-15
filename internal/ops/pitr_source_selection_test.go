package ops

import (
	"context"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// TestSelectFDBBackupSkipsTheWindowCheckByDefault checks that an absent target
// selects the newest backup without a source-cluster lookup. The nil Docker
// client would cause a panic if selection attempted a describe command.
func TestSelectFDBBackupSkipsTheWindowCheckByDefault(t *testing.T) {
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: nil,
	}
	name, err := selectFDBBackup(
		context.Background(),
		drill,
		"tack-rtfdb-run",
		[]string{"backups/20260829T000000Z", "backups/20260830T000000Z"},
	)
	if err != nil {
		t.Fatalf("selection without a target must not check a window: %v", err)
	}

	if name != "20260830T000000Z" {
		t.Fatalf("selection returned %q, want the newest backup's bare name", name)
	}
}

// TestSelectFDBBackupChecksWindowForZeroTarget checks that an explicit zero
// target attempts a window lookup. With this configuration, lookup returns
// a validation error or panics on the nil Docker client rather than selecting
// the latest backup. A lookup error does not establish Docker execution.
func TestSelectFDBBackupChecksWindowForZeroTarget(t *testing.T) {
	zeroInstant := time.Time{}
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: &zeroInstant,
	}

	windowLookupAttempted := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Logf("window lookup panicked with the nil Docker client: %v", recovered)
				windowLookupAttempted = true
			}
		}()
		if _, err := selectFDBBackup(
			context.Background(),
			drill,
			"tack-rtfdb-run",
			[]string{"backups/20260830T000000Z"},
		); err != nil {
			t.Logf("window lookup returned an error: %v", err)
			windowLookupAttempted = true
		}
	}()
	if !windowLookupAttempted {
		t.Fatal(
			"selection treated the explicit zero instant as an absent target and skipped window lookup",
		)
	}
}
