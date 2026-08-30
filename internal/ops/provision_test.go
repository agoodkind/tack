package ops

import (
	"context"
	"errors"
	"testing"

	"goodkind.io/tack/internal/config"
)

// recordingFDBContinuousStart stands in for RunBackupFDBContinuousInit so the
// provision decision is observable without a live cluster, an object store, or
// a Docker daemon. It records what provision handed the starter, which is what
// the real call would have used to reach the cluster.
type recordingFDBContinuousStart struct {
	calls  int
	gotCfg *config.Config
	err    error
}

func (r *recordingFDBContinuousStart) start(_ context.Context, cfg *config.Config) error {
	r.calls++
	r.gotCfg = cfg
	return r.err
}

func TestProvisionStartsFDBContinuousWhenFlagIsOn(t *testing.T) {
	cfg := &config.Config{
		BackupFDBContinuous: true,
		BackupS3BucketMain:  "tack-backups",
	}
	starter := &recordingFDBContinuousStart{}

	if err := provisionFDBContinuous(t.Context(), cfg, starter.start); err != nil {
		t.Fatalf("provisionFDBContinuous: %v", err)
	}

	if starter.calls != 1 {
		t.Fatalf("provision started the stream %d times, want 1", starter.calls)
	}
	if starter.gotCfg != cfg {
		t.Fatal("provision started the stream against a different config")
	}
}

func TestProvisionLeavesFDBContinuousOffWhenFlagIsOff(t *testing.T) {
	cfg := &config.Config{
		BackupFDBContinuous: false,
		BackupS3BucketMain:  "tack-backups",
	}
	starter := &recordingFDBContinuousStart{}

	if err := provisionFDBContinuous(t.Context(), cfg, starter.start); err != nil {
		t.Fatalf("provisionFDBContinuous: %v", err)
	}

	if starter.calls != 0 {
		t.Fatalf("provision started the stream %d times with the flag off, want 0", starter.calls)
	}
}

// A start that fails must fail provision. The whole defect this step closes is
// a protection that reports success while nothing streams, so a swallowed error
// here would reproduce it one layer up. An unreachable object store surfaces as
// a nonzero `fdbbackup start`, which is this case.
func TestProvisionFailsWhenFDBContinuousStartFails(t *testing.T) {
	cfg := &config.Config{
		BackupFDBContinuous: true,
		BackupS3BucketMain:  "tack-backups",
	}
	startErr := errors.New("fdbbackup start exited 1: could not reach blobstore")
	starter := &recordingFDBContinuousStart{err: startErr}

	err := provisionFDBContinuous(t.Context(), cfg, starter.start)

	if err == nil {
		t.Fatal("provision reported success while the stream failed to start")
	}
	if !errors.Is(err, startErr) {
		t.Fatalf("provision lost the start failure: %v", err)
	}
	if starter.calls != 1 {
		t.Fatalf("provision started the stream %d times, want 1", starter.calls)
	}
}
