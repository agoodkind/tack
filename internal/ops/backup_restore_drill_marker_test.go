package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// unansweredMarkerEndpoint refuses connections like a stopped object store.
const unansweredMarkerEndpoint = "http://localhost:1"

// countingMarkerStore counts production-client writes. Unreachable writes
// use a refused connection with one client attempt to test the drill's retry
// loop without the client's jittered retry delays.
type countingMarkerStore struct {
	store     *backupTestStore
	puts      int
	reachable bool
}

func (s *countingMarkerStore) put(key string, body []byte) error {
	s.puts++
	if s.reachable {
		return s.store.putBytes(key, body)
	}

	cfg := s.store.config()
	cfg.BackupS3Endpoint = unansweredMarkerEndpoint
	client := s3.New(
		newBackupS3Client(cfg).Options(),
		func(options *s3.Options) {
			options.RetryMaxAttempts = 1
		},
	)

	return putObjectBytes(context.Background(), client, cfg.BackupS3BucketMain, key, body)
}

// recordMarkerWaits records delays without waiting, invokes onWait after each
// request, and reports whether the context remains active.
func recordMarkerWaits(t *testing.T, onWait func(waits int)) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	waitFunc = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		onWait(len(waits))

		return ctx.Err() == nil
	}
	t.Cleanup(func() { waitFunc = waitUntil })

	return &waits
}

// noMarkerWaitAction leaves store availability unchanged during a delay.
func noMarkerWaitAction(int) {}

// TestRestoreDrillMarkerRetriesAFailedPut checks exponential retry delays and
// successful publication with the original pass time after store recovery.
func TestRestoreDrillMarkerRetriesAFailedPut(t *testing.T) {
	passedAt := time.Date(2026, 9, 5, 3, 30, 0, 0, time.UTC)
	nowFunc = func() time.Time { return passedAt }
	t.Cleanup(func() { nowFunc = time.Now })
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0, reachable: false}
	waits := recordMarkerWaits(
		t,
		func(waits int) {
			if waits == 3 {
				store.reachable = true
			}
		},
	)

	err := recordRestoreDrillRehearsal(
		context.Background(),
		store.put,
		"rt20260905T033000Z-7",
		[]string{"fdb", "yugabyte"},
	)
	if err != nil {
		t.Fatalf("a write must succeed after store recovery within the retry limit: %v", err)
	}

	if store.puts != 4 {
		t.Fatalf("puts = %d, want 3 failures and one success", store.puts)
	}

	wantWaits := []time.Duration{8 * time.Second, 16 * time.Second, 32 * time.Second}
	if len(*waits) != len(wantWaits) {
		t.Fatalf("waits = %v, want %v", *waits, wantWaits)
	}

	for i, want := range wantWaits {
		if (*waits)[i] != want {
			t.Fatalf("waits = %v, want %v", *waits, wantWaits)
		}
	}

	marker, found, err := readBackupStatusMarker(
		context.Background(),
		store.store.getBytes,
		backupStalenessRehearsalName,
	)
	if err != nil || !found {
		t.Fatalf("the check must find the marker: found=%v err=%v", found, err)
	}

	if !marker.At.Equal(passedAt) {
		t.Fatalf("marker at = %s, want the drill's pass time %s", marker.At, passedAt)
	}

	if marker.Detail != "restore drill rt20260905T033000Z-7 passed: fdb, yugabyte" {
		t.Fatalf("marker detail = %q", marker.Detail)
	}
}

// TestRestoreDrillMarkerFailsAfterRetryLimit checks the attempt and
// delay limits, marker-specific error status, and absence of a success marker.
func TestRestoreDrillMarkerFailsAfterRetryLimit(t *testing.T) {
	waits := recordMarkerWaits(t, noMarkerWaitAction)
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0, reachable: false}

	err := recordRestoreDrillRehearsal(
		context.Background(),
		store.put,
		"rt20260906T033000Z-7",
		[]string{"fdb", "yugabyte"},
	)
	if err == nil {
		t.Fatal("a failed marker write must fail the drill")
	}

	if !strings.HasPrefix(
		err.Error(),
		"restore-drill: every leg passed but the rehearsal marker write failed: ",
	) {
		t.Fatalf("the error must identify the marker as the failed step, got: %v", err)
	}

	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the error must include the store's refusal, got: %v", err)
	}

	var coded interface{ OperatorExitStatus() int }
	if !errors.As(err, &coded) || coded.OperatorExitStatus() != restoreDrillMarkerExitStatus {
		t.Fatalf(
			"a marker-only failure must declare exit status %d, got: %#v",
			restoreDrillMarkerExitStatus,
			err,
		)
	}

	if store.puts != restoreDrillMarkerAttempts {
		t.Fatalf("puts = %d, want exactly %d attempts", store.puts, restoreDrillMarkerAttempts)
	}

	if len(*waits) != restoreDrillMarkerAttempts-1 {
		t.Fatalf("waits = %v, want one pause between each pair of attempts", *waits)
	}

	_, found, err := readBackupStatusMarker(
		context.Background(),
		store.store.getBytes,
		backupStalenessRehearsalName,
	)
	if err != nil {
		t.Fatalf("read the rehearsal marker: %v", err)
	}

	if found {
		t.Fatal("failed writes must not publish a marker")
	}
}

// TestRestoreDrillMarkerStopsWhenTheContextEnds checks immediate marker failure
// after cancellation instead of further retry delays.
func TestRestoreDrillMarkerStopsWhenTheContextEnds(t *testing.T) {
	waits := recordMarkerWaits(t, noMarkerWaitAction)
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0, reachable: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := recordRestoreDrillRehearsal(
		ctx,
		store.put,
		"rt20260906T033000Z-8",
		[]string{"fdb", "yugabyte"},
	)
	if err == nil || !strings.Contains(err.Error(), "the rehearsal marker write failed") {
		t.Fatalf("an interrupted drill must still identify marker failure, got: %v", err)
	}

	if store.puts != 1 || len(*waits) != 1 {
		t.Fatalf(
			"puts = %d waits = %d, want one attempt and one refused pause",
			store.puts,
			len(*waits),
		)
	}
}
