//go:build integration

package audit

import (
	"bytes"
	"context"
	"expvar"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// staleHeadroomWeeks is the gauge value QA reported for four weeks after its
// maintenance began failing (TACK-551).
const staleHeadroomWeeks = 13

// TestPartitionManagerReportsHeadroomWhenMaintenanceFails proves a failing
// maintenance run still publishes the real headroom and raises the
// low-headroom alert. A child of audit.events named outside the
// events_pYYYY_MM_DD form makes pg_partman fail, as events_tack336_proof did
// on QA.
func TestPartitionManagerReportsHeadroomWhenMaintenanceFails(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 15)
	pool := fixture.pool
	ctx := context.Background()

	nextWeek := time.Now().UTC().AddDate(0, 0, 7)
	dropPartitionsCovering(t, pool, nextWeek)
	dropPartitionsCovering(t, pool, nextWeek.AddDate(0, 0, 7))
	if _, err := pool.Exec(ctx, `
		CREATE TABLE audit.events_tack336_proof PARTITION OF audit.events
		FOR VALUES FROM ('2031-03-03 00:00:00+00') TO ('2031-03-10 00:00:00+00')
	`); err != nil {
		t.Fatalf("create misnamed child: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err == nil ||
		!strings.Contains(err.Error(), `invalid value "roof"`) {
		t.Fatalf("maintenance error = %v, want invalid value \"roof\"", err)
	}

	// The only partition that starts after now is the 2031 child.
	const wantHeadroom = 1
	telemetry.SetAuditPartitionHeadroomWeeks(staleHeadroomWeeks)
	failuresBefore := maintenanceErrors(t)
	logs :=&lockedBuffer{mu: sync.Mutex{}, buf: bytes.Buffer{}}
	handler := slog.NewJSONHandler(logs, &slog.HandlerOptions{AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil})
	managerCtx := telemetry.WithLogger(ctx, slog.New(handler))
	manager := NewPartitionManager(NewPGPartitionStore(pool), time.Hour)
	manager.Start(managerCtx)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close partition manager: %v", err)
		}
	})

	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		output := logs.String()
		if headroomGauge(t) == wantHeadroom && strings.Contains(output, `"msg":"audit.partition.headroom_low"`) {
			if failures := maintenanceErrors(t); failures <= failuresBefore {
				t.Fatalf("maintenance error count = %d, want more than %d: the gauge must come from a failed run",
					failures, failuresBefore)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("headroom gauge = %d, want %d with audit.partition.headroom_low; logs:\n%s",
				headroomGauge(t), wantHeadroom, output)
		case <-ticker.C:
		}
	}
}

func headroomGauge(t *testing.T) int64 {
	t.Helper()
	gauge, ok := expvar.Get("tack_audit_partition_headroom_weeks").(*expvar.Int)
	if !ok {
		t.Fatalf("expvar tack_audit_partition_headroom_weeks is not registered as an int")
	}
	return gauge.Value()
}

// maintenanceErrors reads the error entry of the exported maintenance counter;
// an absent entry is zero.
func maintenanceErrors(t *testing.T) int64 {
	t.Helper()
	counter, ok := expvar.Get("tack_audit_partition_maintenance_total").(*expvar.Map)
	if !ok {
		t.Fatalf("expvar tack_audit_partition_maintenance_total is not registered as a map")
	}
	failures, ok := counter.Get("error").(*expvar.Int)
	if !ok {
		return 0
	}
	return failures.Value()
}

// lockedBuffer lets the manager goroutine write logs while the test reads them.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
