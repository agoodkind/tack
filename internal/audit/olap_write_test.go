package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// olapColumnTypes is the audit.events_olap column list in insert order, the
// same order ensureClickHouseSchema declares and insertOLAPBatch appends.
var olapColumnTypes = []string{
	"UUID", "Int16", "DateTime64(9, 'UTC')", "UUID", "Int64",
	"UUID", "Int16", "String", "String", "String", "String", "String", "UUID",
	"String", "String", "Nullable(UUID)",
	"String", "String", "String",
}

// columnBindingBatch is a batch whose Append runs every value through the
// driver's real column encoders for the OLAP schema, so a Go value the driver
// cannot bind fails here the way it fails against a server, without a server.
type columnBindingBatch struct {
	chdriver.Batch
	columns []column.Interface
	rows    int
	sent    bool
}

func newColumnBindingBatch(t *testing.T) *columnBindingBatch {
	t.Helper()
	serverContext := &column.ServerContext{Revision: 0, VersionMajor: 0, VersionMinor: 0, VersionPatch: 0, Timezone: time.UTC}
	columns := make([]column.Interface, 0, len(olapColumnTypes))
	for i, columnType := range olapColumnTypes {
		col, err := column.Type(columnType).Column(fmt.Sprintf("c%d", i), serverContext)
		if err != nil {
			t.Fatalf("column %d %s: %v", i, columnType, err)
		}
		columns = append(columns, col)
	}
	return &columnBindingBatch{Batch: nil, columns: columns, rows: 0, sent: false}
}

func (b *columnBindingBatch) Append(values ...any) error {
	if len(values) != len(b.columns) {
		return fmt.Errorf("append %d values to %d columns", len(values), len(b.columns))
	}
	for i, value := range values {
		if err := b.columns[i].AppendRow(value); err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
	}
	b.rows++
	return nil
}

func (b *columnBindingBatch) Send() error {
	b.sent = true
	return nil
}

func (b *columnBindingBatch) Close() error { return nil }

// columnBindingConn hands insertOLAPBatch the binding batch.
type columnBindingConn struct {
	chdriver.Conn
	batch *columnBindingBatch
}

func (c *columnBindingConn) PrepareBatch(context.Context, string, ...chdriver.PrepareBatchOption) (chdriver.Batch, error) {
	return c.batch, nil
}

// TestInsertOLAPBatchBindsEveryProjectedField pins that every value the
// projection appends is one the ClickHouse driver can bind to its column. The
// named Outcome type slipped through unconverted after the outcome column
// landed, and every projection insert on the testbed failed with "converting
// audit.Outcome to String is unsupported", leaving the analytics tier empty.
func TestInsertOLAPBatchBindsEveryProjectedField(t *testing.T) {
	piiRef := uuid.Must(uuid.NewV7())
	event := projectedEvent{
		OrgID: uuid.Must(uuid.NewV7()), Shard: 3, EventTime: time.Now().UTC(), EventID: uuid.Must(uuid.NewV7()), Seq: 7,
		ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5, Action: "ops.test.olap", Outcome: OutcomeError,
		Error: json.RawMessage(`{"code":"command_failed","message":"boom"}`), Extra: json.RawMessage(`{"op_id":"x"}`),
		EntityKind: "system", EntityID: uuid.Must(uuid.NewV7()),
		Context: []byte(`{"org_id":"x"}`), Delta: []byte("null"), PIIRef: &piiRef,
		PrevHash: []byte{1, 2}, RowHash: []byte{3, 4}, IdemKey: "idem",
	}
	batch := newColumnBindingBatch(t)
	conn := &columnBindingConn{Conn: nil, batch: batch}

	if err := insertOLAPBatch(context.Background(), conn, []projectedEvent{event}); err != nil {
		t.Fatalf("insertOLAPBatch: %v", err)
	}
	if batch.rows != 1 || !batch.sent {
		t.Fatalf("rows appended = %d, sent = %t; want 1 row sent", batch.rows, batch.sent)
	}
	if got := batch.columns[8].Row(0, false); got != string(OutcomeError) {
		t.Fatalf("outcome column holds %v, want %q", got, OutcomeError)
	}
	if got, ok := batch.columns[9].Row(0, false).(string); !ok || !strings.Contains(got, "command_failed") {
		t.Fatalf("error column holds %v, want the error JSON", batch.columns[9].Row(0, false))
	}
}
