package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

type fakeStore struct {
	mu       sync.Mutex
	runs     int
	runErr   error
	headroom int
	headErr  error
}

func (f *fakeStore) RunMaintenance(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	return f.runErr
}

func (f *fakeStore) HeadroomWeeks(context.Context, time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headroom, f.headErr
}

func (f *fakeStore) StrayChildren(context.Context) ([]string, error) { return nil, nil }

func (f *fakeStore) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// TestPartitionManagerRunsOnceOnStart verifies boot catch-up: Start triggers one
// maintenance run before any tick fires.
func TestPartitionManagerRunsOnceOnStart(t *testing.T) {
	store := &fakeStore{headroom: 12}
	pm := NewPartitionManager(store, time.Hour)
	ctx := context.Background()
	pm.Start(ctx)
	deadline := time.After(2 * time.Second)
	for store.runCount() < 1 {
		select {
		case <-deadline:
			t.Fatal("maintenance did not run on start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestPartitionManagerCloseIdempotent verifies Close can be called twice safely.
func TestPartitionManagerCloseIdempotent(t *testing.T) {
	pm := NewPartitionManager(&fakeStore{headroom: 12}, time.Hour)
	pm.Start(context.Background())
	if err := pm.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestPartitionMaintenanceMeetsTheAlertFloor runs maintenance on a migrated
// ledger and checks that the headroom it leaves does not trip the alert.
func TestPartitionMaintenanceMeetsTheAlertFloor(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testenv.Ledger(t))
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := NewPGPartitionStore(pool)
	if err := store.RunMaintenance(ctx); err != nil {
		t.Fatalf("run maintenance: %v", err)
	}
	headroom, err := store.HeadroomWeeks(ctx, clock.Now().UTC())
	if err != nil {
		t.Fatalf("headroom: %v", err)
	}
	if headroom < partitionHeadroomAlertFloor {
		t.Fatalf("headroom after maintenance = %d weeks, below the alert floor of %d",
			headroom, partitionHeadroomAlertFloor)
	}
}

// TestPartitionManagerRunErrorDoesNotPanic verifies a maintenance failure is
// swallowed (logged) and the worker keeps running.
func TestPartitionManagerRunErrorDoesNotPanic(t *testing.T) {
	store := &fakeStore{runErr: errors.New("boom"), headroom: 0}
	pm := NewPartitionManager(store, time.Hour)
	pm.Start(context.Background())
	deadline := time.After(2 * time.Second)
	for store.runCount() < 1 {
		select {
		case <-deadline:
			t.Fatal("maintenance did not run")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
