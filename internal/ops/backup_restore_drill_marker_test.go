package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// unansweredMarkerEndpoint uses an address without a listener. Connections
// fail with the same refusal as a stopped object store.
const unansweredMarkerEndpoint = "http://localhost:1"

// countingMarkerStore writes through the production client and counts calls.
// When reachable is false, unansweredMarkerEndpoint refuses each connection.
// When reachable is true, writes use the test object store. Failed writes use
// one client attempt instead of three. The drill's retry loop supplies the
// backoff under test; client jitter would add seconds without changing the
// error that loop receives.
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

// recordMarkerWaits replaces the package wait function with a recorder. Each
// call records the pause, invokes onWait with the count so far, and reports
// whether the context remains active. Tests can check the backoff without
// waiting.
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

// noMarkerWaitAction leaves the store unchanged during a pause.
func noMarkerWaitAction(int) {}

// TestRestoreDrillMarkerRetriesAFailedPut makes the store available during
// the third pause after refusing the first writes. The marker must succeed
// after doubling delays and retain the drill's pass time rather than the
// successful attempt time.
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
		t.Fatalf("a put that recovers within the attempts must land: %v", err)
	}

	if store.puts != 4 {
		t.Fatalf("puts = %d, want 3 failures and the one that landed", store.puts)
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

// TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails keeps the store
// unavailable for every bounded attempt. The drill must report marker failure
// after the expected attempts and pauses. It must not write a marker that the
// staleness check could interpret as a successful rehearsal.
func TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails(t *testing.T) {
	waits := recordMarkerWaits(t, noMarkerWaitAction)
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0, reachable: false}

	err := recordRestoreDrillRehearsal(
		context.Background(),
		store.put,
		"rt20260906T033000Z-7",
		[]string{"fdb", "yugabyte"},
	)
	if err == nil {
		t.Fatal("a marker that never lands must fail the drill")
	}

	if !strings.HasPrefix(
		err.Error(),
		"restore-drill: every leg passed but the rehearsal marker did not land: ",
	) {
		t.Fatalf("the error must name the marker as the failed step, got: %v", err)
	}

	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the error must carry the store's refusal, got: %v", err)
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
		t.Fatalf(
			"puts = %d, want every one of the %d attempts and no more",
			store.puts,
			restoreDrillMarkerAttempts,
		)
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
		t.Fatal("no marker may land when every put was refused")
	}
}

// TestRestoreDrillMarkerStopsWhenTheContextEnds requires the drill to stop
// retrying when the context ends between attempts. It must identify the marker
// failure without taking the remaining pauses.
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
	if err == nil || !strings.Contains(err.Error(), "the rehearsal marker did not land") {
		t.Fatalf("an interrupted drill must still name the marker, got: %v", err)
	}

	if store.puts != 1 || len(*waits) != 1 {
		t.Fatalf(
			"puts = %d waits = %d, want one attempt and one refused pause",
			store.puts,
			len(*waits),
		)
	}
}
