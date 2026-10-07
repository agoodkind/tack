//go:build integration

package audit_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"expvar"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/testenv"
)

const (
	strayReportChildName  = "events_tack556_stray"
	strayReportChildTable = "audit." + strayReportChildName
	strayReportCreateSQL  = `CREATE TABLE ` + strayReportChildTable + ` PARTITION OF audit.events
		FOR VALUES FROM ('2033-03-01 00:00:00+00') TO ('2033-03-08 00:00:00+00')`
)

type strayChildLogRecord struct {
	Message string   `json:"msg"`
	Names   []string `json:"names"`
}

type strayReportLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *strayReportLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *strayReportLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPartitionManagerReportsStrayChildCreatedWithSchemaGuardDisabled(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testenv.Ledger(t))
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	setGuardTriggers(ctx, t, pool, "DISABLE")
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+strayReportChildTable); err != nil {
			t.Errorf("drop %s: %v", strayReportChildTable, err)
		}
		setGuardTriggers(context.Background(), t, pool, "ENABLE")
	})
	if _, err := pool.Exec(ctx, strayReportCreateSQL); err != nil {
		t.Fatalf("create the stray child with the schema guard disabled: %v", err)
	}

	telemetry.SetAuditPartitionStrayChildren(0)
	logs := &strayReportLogBuffer{mu: sync.Mutex{}, buf: bytes.Buffer{}}
	handler := slog.NewJSONHandler(logs, &slog.HandlerOptions{AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil})
	manager := audit.NewPartitionManager(audit.NewPGPartitionStore(pool), time.Hour)
	manager.Start(telemetry.WithLogger(ctx, slog.New(handler)))
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
		names := strayChildLogNames(t, output)
		if slices.Contains(names, strayReportChildName) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("audit.partition.stray_child names = %v, want %s; logs:\n%s", names, strayReportChildName, output)
		case <-ticker.C:
		}
	}
	gauge, ok := expvar.Get("tack_audit_partition_stray_children").(*expvar.Int)
	if !ok {
		t.Fatalf("expvar tack_audit_partition_stray_children is not registered as an int")
	}
	if gauge.Value() < 1 {
		t.Fatalf("stray children gauge = %d, want at least 1", gauge.Value())
	}
}

func strayChildLogNames(t *testing.T, output string) []string {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var record strayChildLogRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode log line %q: %v", scanner.Text(), err)
		}
		if record.Message == "audit.partition.stray_child" {
			return record.Names
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan logs: %v", err)
	}
	return nil
}
