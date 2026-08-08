package audit

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// insertOLAPBatch appends a batch of projected events into ClickHouse
// audit.events_olap. It is the single insert path shared by the live consumer
// projection and the reconcile backfill (TACK-316), so both write the exact
// same column shape and cannot drift. Best effort: failures log at WARN, not
// ERROR, because the canonical Yugabyte store already holds these rows.
func insertOLAPBatch(ctx context.Context, ch chdriver.Conn, batch []projectedEvent) error {
	bw, err := ch.PrepareBatch(ctx, `INSERT INTO audit.events_olap`)
	if err != nil {
		slog.WarnContext(ctx, "audit.olap.prepare_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clickhouse prepare: %w", err)
	}
	defer bw.Close()
	for _, p := range batch {
		err := bw.Append(
			p.OrgID, p.Shard, p.EventTime, p.EventID, p.Seq,
			p.ActorID, p.ActorKind, p.Action, p.Outcome, p.EntityKind, p.EntityID,
			string(p.Context), string(p.Delta), p.PIIRef,
			hex.EncodeToString(p.PrevHash), hex.EncodeToString(p.RowHash), p.IdemKey,
		)
		if err != nil {
			slog.WarnContext(ctx, "audit.olap.append_failed", slog.String("err", err.Error()))
			return fmt.Errorf("clickhouse append: %w", err)
		}
	}
	if err := bw.Send(); err != nil {
		slog.WarnContext(ctx, "audit.olap.send_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clickhouse send: %w", err)
	}
	return nil
}
