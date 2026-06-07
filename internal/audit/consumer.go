package audit

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// ConsumerConfig collects every input the audit-consumer binary needs.
// Defaults match the cutover design in docs/plans/audit-kafka-cutover.md.
type ConsumerConfig struct {
	Brokers         []string
	Topic           string
	GroupID         string
	BatchSize       int
	PollInterval    time.Duration
	YugabyteDSN     string
	ClickHouseDSN   string
	SigningKeyPath  string
	NotarizerPeriod time.Duration

	// LagWarnMessages is the per-partition lag threshold above which the
	// consumer emits a `consumer.lag.high` warning every poll. Zero falls
	// back to 1000 (the operator-default for Wave 1).
	LagWarnMessages int64

	// SummaryEvery is how many records between batched
	// `consumer.processed` debug summaries. Zero falls back to 100.
	SummaryEvery int
}

// Consumer projects audit events from Kafka into Yugabyte (audit.events) and
// ClickHouse (audit.events_olap) and runs an embedded notarizer goroutine. The
// consumer is the only writer of audit.events and audit.chain_heads.
type Consumer struct {
	cfg     ConsumerConfig
	kclient *kgo.Client
	ybpool  *pgxpool.Pool
	ch      chdriver.Conn

	notarizer *Notarizer

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	summary processedSummary
}

// processedSummary buffers per-batch counts so the consumer emits one
// `consumer.processed` debug line per N records rather than one per record.
type processedSummary struct {
	count        int
	verbCounts   map[string]int
	commitOffset int64
}

// NewConsumer opens every external connection and prepares the embedded
// notarizer. The returned Consumer is ready for Start. Callers must Close.
func NewConsumer(ctx context.Context, cfg ConsumerConfig) (*Consumer, error) {
	cfg = applyConsumerDefaults(cfg)
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("audit consumer: brokers required")
	}
	if cfg.YugabyteDSN == "" {
		return nil, errors.New("audit consumer: yugabyte DSN required")
	}

	ybpool, err := pgxpool.New(ctx, cfg.YugabyteDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.yugabyte_pool_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit consumer yugabyte pool: %w", err)
	}
	if err := ybpool.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.yugabyte_ping_failed", slog.String("err", err.Error()))
		ybpool.Close()
		return nil, fmt.Errorf("audit consumer yugabyte ping: %w", err)
	}

	ch, err := openClickHouse(ctx, cfg.ClickHouseDSN)
	if err != nil {
		ybpool.Close()
		return nil, err
	}

	kclient, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.GroupID),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.DisableAutoCommit(),
		kgo.FetchMaxWait(cfg.PollInterval),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.kafka_client_failed", slog.String("err", err.Error()))
		ybpool.Close()
		if ch != nil {
			_ = ch.Close()
		}
		return nil, fmt.Errorf("audit consumer kafka client: %w", err)
	}

	c := &Consumer{
		cfg:       cfg,
		kclient:   kclient,
		ybpool:    ybpool,
		ch:        ch,
		notarizer: nil,
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
		once:      sync.Once{},
		summary: processedSummary{
			count:        0,
			verbCounts:   make(map[string]int),
			commitOffset: 0,
		},
	}

	if cfg.SigningKeyPath != "" {
		n, nerr := NewNotarizer(ctx, cfg.YugabyteDSN, NotarizerConfig{
			SigningKeyPath: cfg.SigningKeyPath,
			Period:         cfg.NotarizerPeriod,
		})
		if nerr != nil {
			slog.ErrorContext(ctx, "audit.consumer.notarizer_failed", slog.String("err", nerr.Error()))
			c.closeResources()
			return nil, fmt.Errorf("audit consumer notarizer: %w", nerr)
		}
		c.notarizer = n
	}

	return c, nil
}

func applyConsumerDefaults(cfg ConsumerConfig) ConsumerConfig {
	if cfg.Topic == "" {
		cfg.Topic = "audit.events.v1"
	}
	if cfg.GroupID == "" {
		cfg.GroupID = "tack-audit-projector"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.NotarizerPeriod <= 0 {
		cfg.NotarizerPeriod = 60 * time.Second
	}
	if cfg.LagWarnMessages <= 0 {
		cfg.LagWarnMessages = 1000
	}
	if cfg.SummaryEvery <= 0 {
		cfg.SummaryEvery = 100
	}
	return cfg
}

func openClickHouse(ctx context.Context, dsn string) (chdriver.Conn, error) {
	if dsn == "" {
		return nil, nil
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_dsn_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit consumer clickhouse parse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit consumer clickhouse open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_ping_failed", slog.String("err", err.Error()))
		_ = conn.Close()
		return nil, fmt.Errorf("audit consumer clickhouse ping: %w", err)
	}
	if err := ensureClickHouseSchema(ctx, conn); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_schema_failed", slog.String("err", err.Error()))
		_ = conn.Close()
		return nil, fmt.Errorf("audit consumer clickhouse schema: %w", err)
	}
	return conn, nil
}

// Start runs the poll loop and the embedded notarizer until ctx is canceled
// or Close is called.
func (c *Consumer) Start(ctx context.Context) {
	if c.notarizer != nil {
		c.notarizer.Start(ctx)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "audit.consumer.loop_panic",
					slog.Any("panic", r),
					slog.String("err", fmt.Sprintf("%v", r)),
				)
			}
		}()
		c.loop(ctx)
	}()
}

// Close signals the loop to stop, drains in-flight work, and releases every
// underlying resource. Idempotent.
func (c *Consumer) Close() error {
	c.once.Do(func() { close(c.stop) })
	<-c.stopped
	if c.notarizer != nil {
		_ = c.notarizer.Close()
	}
	c.closeResources()
	return nil
}

func (c *Consumer) closeResources() {
	if c.kclient != nil {
		c.kclient.Close()
	}
	if c.ybpool != nil {
		c.ybpool.Close()
	}
	if c.ch != nil {
		_ = c.ch.Close()
	}
}

func (c *Consumer) loop(ctx context.Context) {
	defer close(c.stopped)
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		default:
		}

		pollStart := monoStart()
		fetchCtx, cancel := context.WithTimeout(ctx, c.cfg.PollInterval+5*time.Second)
		fetches := c.kclient.PollRecords(fetchCtx, c.cfg.BatchSize)
		cancel()

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, fe := range errs {
				slog.ErrorContext(ctx, "audit.consumer.fetch_err",
					slog.String("topic", fe.Topic),
					slog.Int("partition", int(fe.Partition)),
					slog.String("err", fe.Err.Error()),
				)
			}
		}

		c.observeLag(ctx, fetches)

		if fetches.NumRecords() == 0 {
			continue
		}

		batches := groupByPartition(fetches)
		for tp, records := range batches {
			if err := c.projectBatch(ctx, tp, records); err != nil {
				telemetry.IncAuditConsumerProcessed("error", int64(len(records)))
				slog.ErrorContext(ctx, "audit.consumer.project_failed",
					slog.String("topic", tp.Topic),
					slog.Int("partition", int(tp.Partition)),
					slog.String("err", err.Error()),
				)
				continue
			}
			last := records[len(records)-1]
			c.kclient.MarkCommitRecords(last)
			telemetry.IncAuditConsumerProcessed("ok", int64(len(records)))
			telemetry.SetAuditConsumerOffsetCommitted(tp.Topic, tp.Partition, last.Offset+1)
			c.recordSummary(ctx, records, last.Offset+1)
		}

		if err := c.kclient.CommitMarkedOffsets(ctx); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		}

		telemetry.ObserveAuditConsumerBatchLatency(sinceMs(pollStart))
	}
}

// observeLag publishes the current lag for every partition the broker
// returned, and emits one `consumer.lag.high` warning per partition that
// crosses the configured threshold. Lag is HighWatermark minus the
// just-consumed offset; when no records were returned but the partition
// is non-empty, we still record the gap so the operator sees backlog
// growth even on idle polls.
func (c *Consumer) observeLag(ctx context.Context, fetches kgo.Fetches) {
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		var lastOffset int64 = -1
		for _, r := range p.Records {
			if r.Offset > lastOffset {
				lastOffset = r.Offset
			}
		}
		var lag int64
		if lastOffset >= 0 {
			lag = p.HighWatermark - (lastOffset + 1)
		} else {
			lag = p.HighWatermark
		}
		if lag < 0 {
			lag = 0
		}
		telemetry.SetAuditConsumerLag(p.Topic, p.Partition, lag)
		if lag >= c.cfg.LagWarnMessages {
			slog.WarnContext(ctx, "consumer.lag.high",
				slog.String("topic", p.Topic),
				slog.Int("partition", int(p.Partition)),
				slog.Int64("lag", lag),
			)
		}
	})
}

// recordSummary folds the just-projected batch into the running summary
// and emits a `consumer.processed` debug line every cfg.SummaryEvery
// records. Verb breakdown is the top-5 verbs by count; ties break
// alphabetically so the output is deterministic.
func (c *Consumer) recordSummary(ctx context.Context, records []*kgo.Record, commitOffset int64) {
	if c.summary.verbCounts == nil {
		c.summary.verbCounts = make(map[string]int)
	}
	c.summary.commitOffset = commitOffset
	for _, r := range records {
		c.summary.count++
		c.summary.verbCounts[verbFromRecord(r)]++
	}
	if c.summary.count < c.cfg.SummaryEvery {
		return
	}
	telemetry.L(ctx).Debug("consumer.processed",
		slog.Int("count", c.summary.count),
		slog.Any("verb_breakdown_top5", topVerbs(c.summary.verbCounts, 5)),
		slog.Int64("commit_offset", c.summary.commitOffset),
	)
	c.summary.count = 0
	c.summary.verbCounts = make(map[string]int)
}

// verbFromRecord pulls the verb out of a Kafka record by peeking at the
// JSON value. The decode is best-effort; an unparseable payload falls
// back to "_unparseable" so the summary still surfaces the volume.
func verbFromRecord(rec *kgo.Record) string {
	var probe struct {
		Verb string `json:"verb"`
	}
	if err := json.Unmarshal(rec.Value, &probe); err != nil || probe.Verb == "" {
		return "_unparseable"
	}
	return probe.Verb
}

// topVerbs returns the top-N verbs by count from the summary map. Output
// is a slice of "verb=count" strings ordered by count desc, then verb asc,
// so the slog line stays readable.
func topVerbs(counts map[string]int, n int) []string {
	type pair struct {
		verb  string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for v, c := range counts {
		pairs = append(pairs, pair{v, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].verb < pairs[j].verb
	})
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, fmt.Sprintf("%s=%d", p.verb, p.count))
	}
	return out
}

type topicPartition struct {
	Topic     string
	Partition int32
}

func groupByPartition(fetches kgo.Fetches) map[topicPartition][]*kgo.Record {
	out := map[topicPartition][]*kgo.Record{}
	iter := fetches.RecordIter()
	for !iter.Done() {
		r := iter.Next()
		key := topicPartition{Topic: r.Topic, Partition: r.Partition}
		out[key] = append(out[key], r)
	}
	return out
}

// projectBatch applies all records for one partition, retrying the whole batch
// when a concurrent consumer advanced a shard this batch touches. The
// ClickHouse projection runs once after the Yugabyte commit and is best effort.
func (c *Consumer) projectBatch(ctx context.Context, tp topicPartition, records []*kgo.Record) error {
	const maxChainRetries = 8
	for attempt := 1; ; attempt++ {
		projected, err := c.projectBatchOnce(ctx, tp, records)
		if err == nil {
			c.writeClickHouseBatch(ctx, projected)
			return nil
		}
		if attempt < maxChainRetries && isRetryableChainErr(err) {
			slog.WarnContext(ctx, "audit.consumer.chain_retry",
				slog.String("topic", tp.Topic),
				slog.Int("partition", int(tp.Partition)),
				slog.Int("attempt", attempt),
			)
			continue
		}
		return err
	}
}

// projectBatchOnce projects one partition's records in a single Yugabyte
// transaction. The Kafka offset row in audit.consumer_offsets is updated in the
// same transaction so a crash before commit replays the same records. It
// returns the projected events for the best-effort ClickHouse write.
func (c *Consumer) projectBatchOnce(ctx context.Context, tp topicPartition, records []*kgo.Record) ([]projectedEvent, error) {
	tx, err := c.ybpool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.begin_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	projected := make([]projectedEvent, 0, len(records))
	for _, rec := range records {
		pe, perr := c.projectOne(ctx, tx, rec)
		if perr != nil {
			if errors.Is(perr, errAlreadyProjected) {
				continue
			}
			if errors.Is(perr, errMalformedPayload) {
				writeDLQ(ctx, tx, rec, perr)
				continue
			}
			if isRetryableChainErr(perr) {
				return nil, perr
			}
			slog.ErrorContext(ctx, "audit.consumer.project_record_failed",
				slog.String("err", perr.Error()),
				slog.Int64("offset", rec.Offset),
			)
			return nil, fmt.Errorf("project record offset=%d: %w", rec.Offset, perr)
		}
		projected = append(projected, pe)
	}

	last := records[len(records)-1]
	_, err = tx.Exec(ctx, `
		INSERT INTO audit.consumer_offsets (consumer_group, topic, partition, "offset", updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (consumer_group, topic, partition) DO UPDATE
		    SET "offset"   = EXCLUDED."offset",
		        updated_at = EXCLUDED.updated_at
	`, c.cfg.GroupID, tp.Topic, tp.Partition, last.Offset+1)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.offset_upsert_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("offset upsert: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("commit: %w", err)
	}

	return projected, nil
}

// writeClickHouseBatch projects a committed batch into ClickHouse. The OLAP
// write is best effort, so a ClickHouse outage never blocks chain advancement.
func (c *Consumer) writeClickHouseBatch(ctx context.Context, projected []projectedEvent) {
	if c.ch == nil || len(projected) == 0 {
		return
	}
	cerr := c.writeClickHouse(ctx, projected)
	if cerr != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_write_failed",
			slog.String("err", cerr.Error()),
			slog.Int("count", len(projected)),
		)
	}
}

var (
	errMalformedPayload = errors.New("malformed payload")
	errAlreadyProjected = errors.New("event already projected")
)

type projectedEvent struct {
	OrgID      uuid.UUID
	Shard      int16
	EventTime  time.Time
	EventID    uuid.UUID
	Seq        int64
	ActorID    uuid.UUID
	ActorKind  int16
	Action     string
	EntityKind string
	EntityID   uuid.UUID
	Context    []byte
	Delta      []byte
	PIIRef     *uuid.UUID
	PrevHash   []byte
	RowHash    []byte
	IdemKey    string
}

// projectOne writes one event's PII row, then appends it to audit.events and
// advances audit.chain_heads through the shared chain helper.
func (c *Consumer) projectOne(ctx context.Context, tx pgx.Tx, rec *kgo.Record) (projectedEvent, error) {
	prepared, err := unmarshalRecord(ctx, rec)
	if err != nil {
		return projectedEvent{}, err
	}
	piiRef, err := writePIIRow(ctx, tx, prepared.Event.Actor)
	if err != nil {
		return projectedEvent{}, err
	}
	prepared.PIIRef = piiRef
	return c.advanceChain(ctx, tx, prepared)
}

// preparedEvent is the post-decode, pre-chain shape of a Kafka record. It
// caches the json marshals so chain advancement does not redo them.
type preparedEvent struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	ContextJSON []byte
	DeltaJSON   []byte
	PIIRef      uuid.UUID
}

// unmarshalRecord decodes the Kafka record value into an Event and prepares
// derived fields. Errors here are treated as malformed-payload signals so
// the consumer can route the record to the dead-letter table.
func unmarshalRecord(ctx context.Context, rec *kgo.Record) (preparedEvent, error) {
	var ev Event
	if err := json.Unmarshal(rec.Value, &ev); err != nil {
		slog.WarnContext(ctx, "audit.consumer.malformed_json", slog.String("err", err.Error()))
		return preparedEvent{}, fmt.Errorf("%w: %w", errMalformedPayload, err)
	}
	if ev.OccurredAt.IsZero() {
		slog.WarnContext(ctx, "audit.consumer.malformed_no_occurred_at")
		return preparedEvent{}, fmt.Errorf("%w: occurred_at missing", errMalformedPayload)
	}
	canonicalizeCorrelation(&ev)

	if ev.EventID == uuid.Nil {
		slog.WarnContext(ctx, "audit.consumer.malformed_no_event_id")
		return preparedEvent{}, fmt.Errorf("%w: event_id missing", errMalformedPayload)
	}
	eventID := ev.EventID
	shard := shardOf(ev.Actor.ID, eventID)

	contextJSON, err := json.Marshal(ev.Context)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.marshal_context_failed", slog.String("err", err.Error()))
		return preparedEvent{}, fmt.Errorf("marshal context: %w", err)
	}
	deltaJSON := []byte("null")
	if ev.Delta != nil {
		deltaJSON, err = json.Marshal(ev.Delta)
		if err != nil {
			slog.ErrorContext(ctx, "audit.consumer.marshal_delta_failed", slog.String("err", err.Error()))
			return preparedEvent{}, fmt.Errorf("marshal delta: %w", err)
		}
	}
	return preparedEvent{
		Event:       ev,
		EventID:     eventID,
		Shard:       shard,
		ContextJSON: contextJSON,
		DeltaJSON:   deltaJSON,
		PIIRef:      uuid.Nil,
	}, nil
}

// advanceChain appends the prepared event to audit.events and advances its
// (org, shard) head through the shared compare-and-swap helper, so two
// consumers on the same chain cannot fork it. Returns errAlreadyProjected when
// the idempotency index rejects a redelivered event, and errChainConflict when
// the head moved underneath this writer.
func (c *Consumer) advanceChain(ctx context.Context, tx pgx.Tx, p preparedEvent) (projectedEvent, error) {
	res, err := appendChainRow(ctx, tx, chainAppendInput{
		Event:       p.Event,
		EventID:     p.EventID,
		Shard:       p.Shard,
		PIIRef:      p.PIIRef,
		ContextJSON: p.ContextJSON,
		DeltaJSON:   p.DeltaJSON,
	})
	if err != nil {
		return projectedEvent{}, err
	}
	return buildProjectedEvent(p, res.Seq, res.PrevHash, res.RowHash), nil
}

func buildProjectedEvent(p preparedEvent, seq int64, prevHash, rowHash []byte) projectedEvent {
	var piiPtr *uuid.UUID
	if p.PIIRef != uuid.Nil {
		v := p.PIIRef
		piiPtr = &v
	}
	return projectedEvent{
		OrgID:      p.Event.Context.OrgID,
		Shard:      p.Shard,
		EventTime:  p.Event.OccurredAt.UTC(),
		EventID:    p.EventID,
		Seq:        seq,
		ActorID:    p.Event.Actor.ID,
		ActorKind:  actorKindCode(p.Event.Actor.Type),
		Action:     p.Event.Verb,
		EntityKind: p.Event.Entity.Type,
		EntityID:   p.Event.Entity.ID,
		Context:    p.ContextJSON,
		Delta:      p.DeltaJSON,
		PIIRef:     piiPtr,
		PrevHash:   prevHash,
		RowHash:    rowHash,
		IdemKey:    p.Event.IdempotencyKey,
	}
}

func writePIIRow(ctx context.Context, tx pgx.Tx, actor Actor) (uuid.UUID, error) {
	if !hasPII(actor) {
		return uuid.Nil, nil
	}
	piiRef := uuid.Must(uuid.NewV7())
	piiPayload, err := json.Marshal(map[string]string{
		"email":           actor.Email,
		"name":            actor.Name,
		"ip":              actor.IP,
		"ua":              actor.UserAgent,
		"api_token_label": actor.APITokenLabel,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.pii_marshal_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("marshal pii: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.pii (pii_ref, payload, redacted)
		VALUES ($1, $2, false)
		ON CONFLICT DO NOTHING
	`, piiRef, piiPayload); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.pii_insert_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("pii insert: %w", err)
	}
	return piiRef, nil
}

// rowHashInput collects every field that contributes to one event's
// hash-chain payload. The struct exists so the call site avoids inline
// map[string]any and the lint stays clean.
type rowHashInput struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	Seq         int64
	PIIRef      uuid.UUID
	ContextJSON []byte
	DeltaJSON   []byte
	LastHash    []byte
}

// hashRowForEvent matches YBRecorder's chain payload byte-for-byte. Any
// drift breaks the Wave 1 parity gate.
func hashRowForEvent(in rowHashInput) ([]byte, error) {
	payload := make(map[string]any, 14)
	payload["org_id"] = in.Event.Context.OrgID
	payload["shard"] = in.Shard
	payload["seq"] = in.Seq
	payload["event_id"] = in.EventID
	payload["event_time"] = in.Event.OccurredAt.UTC().Format(time.RFC3339Nano)
	payload["actor_id"] = in.Event.Actor.ID
	payload["actor_kind"] = in.Event.Actor.Type
	payload["action"] = in.Event.Verb
	payload["entity_kind"] = in.Event.Entity.Type
	payload["entity_id"] = in.Event.Entity.ID
	payload["pii_ref"] = in.PIIRef
	payload["context"] = json.RawMessage(in.ContextJSON)
	payload["delta"] = json.RawMessage(in.DeltaJSON)
	payload["idempotency"] = in.Event.IdempotencyKey
	return hashRow(in.LastHash, payload)
}

// piiRefArg returns the value to bind for audit.events_v2.pii_ref. The pgx
// adapter treats a typed nil interface as NULL; uuid.Nil written literally
// would produce a non-NULL all-zeros UUID, so we route through pgtype.
func piiRefArg(ref uuid.UUID) pgtype.UUID {
	if ref == uuid.Nil {
		return pgtype.UUID{Bytes: [16]byte{}, Valid: false}
	}
	return pgtype.UUID{Bytes: ref, Valid: true}
}

// writeDLQ records a malformed Kafka payload into audit.events_dlq so the
// operator can investigate. Best effort. Failures are logged.
func writeDLQ(ctx context.Context, tx pgx.Tx, rec *kgo.Record, cause error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit.events_dlq (received_at, topic, partition, "offset", payload, error)
		VALUES (now(), $1, $2, $3, $4, $5)
		ON CONFLICT (topic, partition, "offset") DO NOTHING
	`, rec.Topic, rec.Partition, rec.Offset, rec.Value, cause.Error())
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_write_failed",
			slog.String("err", err.Error()),
			slog.String("topic", rec.Topic),
			slog.Int("partition", int(rec.Partition)),
			slog.Int64("offset", rec.Offset),
		)
	}
}

// ensureClickHouseSchema creates the audit database and audit.events_olap
// table when missing. The OLAP shape mirrors the Yugabyte audit.events fields.
func ensureClickHouseSchema(ctx context.Context, conn chdriver.Conn) error {
	if err := conn.Exec(ctx, `CREATE DATABASE IF NOT EXISTS audit`); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_db_create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create database: %w", err)
	}
	stmt := `
		CREATE TABLE IF NOT EXISTS audit.events_olap (
		    org_id          UUID,
		    shard           Int16,
		    event_time      DateTime64(9, 'UTC'),
		    event_id        UUID,
		    seq             Int64,
		    actor_id        UUID,
		    actor_kind      Int16,
		    action          String,
		    entity_kind     String,
		    entity_id       UUID,
		    context         String,
		    delta           String,
		    pii_ref         Nullable(UUID),
		    prev_hash       String,
		    row_hash        String,
		    idempotency_key String
		) ENGINE = MergeTree
		PARTITION BY toYYYYMMDD(event_time)
		ORDER BY (org_id, shard, event_time, event_id)
	`
	if err := conn.Exec(ctx, stmt); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_table_create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// writeClickHouse projects a batch into ClickHouse. The OLAP write is best
// effort. The canonical store is Yugabyte audit.events.
func (c *Consumer) writeClickHouse(ctx context.Context, batch []projectedEvent) error {
	bw, err := c.ch.PrepareBatch(ctx, `INSERT INTO audit.events_olap`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_prepare_failed", slog.String("err", err.Error()))
		return fmt.Errorf("prepare: %w", err)
	}
	defer bw.Close()
	for _, p := range batch {
		err := bw.Append(
			p.OrgID, p.Shard, p.EventTime, p.EventID, p.Seq,
			p.ActorID, p.ActorKind, p.Action, p.EntityKind, p.EntityID,
			string(p.Context), string(p.Delta), p.PIIRef,
			hex.EncodeToString(p.PrevHash), hex.EncodeToString(p.RowHash), p.IdemKey,
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.consumer.clickhouse_append_failed", slog.String("err", err.Error()))
			return fmt.Errorf("append: %w", err)
		}
	}
	if err := bw.Send(); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_send_failed", slog.String("err", err.Error()))
		return fmt.Errorf("send: %w", err)
	}
	return nil
}
