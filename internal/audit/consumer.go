package audit

import (
	"context"
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

	// ReconcilePeriod and ReconcileWindow drive the ClickHouse backfill: every
	// period, audit.events rows in the trailing window that are missing from
	// audit.events_olap are re-projected, so a ClickHouse outage self-heals
	// (TACK-316). Zero falls back to 30m / 24h.
	ReconcilePeriod time.Duration
	ReconcileWindow time.Duration

	// LagWarnMessages is the per-partition lag threshold above which the
	// consumer emits a `consumer.lag.high` warning every poll. Zero falls
	// back to 1000 (the operator-default for Wave 1).
	LagWarnMessages int64

	// SummaryEvery is how many records between batched
	// `consumer.processed` debug summaries. Zero falls back to 100.
	SummaryEvery int

	// PartitionPeriod is how often the partition-manager runs pg_partman
	// maintenance for audit.events. Zero falls back to 24h.
	PartitionPeriod time.Duration

	// TopicRetention is how long the broker keeps an audit record nobody has
	// consumed. The consumer sets it on the topic at startup. Zero falls back
	// to one year: the topic is the buffer that survives a consumer outage,
	// and the broker default of seven days lost fifteen days of events
	// (TACK-336).
	TopicRetention time.Duration
}

// defaultTopicRetention is one year, well past any outage the operator would
// leave unattended, while still bounded so a broker disk cannot fill without
// end.
const defaultTopicRetention = 365 * 24 * time.Hour

// Consumer projects audit events from Kafka into Yugabyte (audit.events) and
// ClickHouse (audit.events_olap) and runs an embedded notarizer goroutine. The
// consumer is the only writer of audit.events and audit.chain_heads.
type Consumer struct {
	cfg     ConsumerConfig
	kclient *kgo.Client
	ybpool  *pgxpool.Pool
	ch      chdriver.Conn

	notarizer  *Notarizer
	reconciler *Reconciler
	partitions *PartitionManager

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
	if err := pingYugabyteUntilReady(ctx, ybpool); err != nil {
		ybpool.Close()
		return nil, err
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

	if err := ensureAuditTopic(ctx, kclient, cfg.Topic, cfg.TopicRetention); err != nil {
		kclient.Close()
		ybpool.Close()
		if ch != nil {
			_ = ch.Close()
		}
		return nil, err
	}

	c := &Consumer{
		cfg:        cfg,
		kclient:    kclient,
		ybpool:     ybpool,
		ch:         ch,
		notarizer:  nil,
		reconciler: nil,
		partitions: nil,
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
		once:       sync.Once{},
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

	if c.ch != nil {
		c.reconciler = NewReconciler(ybpool, ch, cfg.ReconcilePeriod, cfg.ReconcileWindow)
	}
	c.partitions = NewPartitionManager(NewPGPartitionStore(ybpool), cfg.PartitionPeriod)

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
	if cfg.PartitionPeriod <= 0 {
		cfg.PartitionPeriod = 24 * time.Hour
	}
	if cfg.TopicRetention <= 0 {
		cfg.TopicRetention = defaultTopicRetention
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
	if c.reconciler != nil {
		c.reconciler.Start(ctx)
	}
	if c.partitions != nil {
		c.partitions.Start(ctx)
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
	if c.reconciler != nil {
		_ = c.reconciler.Close()
	}
	if c.partitions != nil {
		_ = c.partitions.Close()
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
				// On an idle topic the per-poll deadline elapses and franz-go
				// injects a fake fetch carrying the poll context's error (empty
				// topic, partition -1). That is the expected "nothing arrived"
				// signal, not a fetch failure, so skip it and only log real
				// per-partition errors. A genuine fatal partition error is
				// surfaced separately and still logged here.
				if errors.Is(fe.Err, context.DeadlineExceeded) || errors.Is(fe.Err, context.Canceled) {
					continue
				}
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
		pe, landed, perr := c.projectRecord(ctx, tx, rec)
		if perr != nil {
			return nil, perr
		}
		if landed {
			projected = append(projected, pe)
		}
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

// projectRecord projects one record inside a savepoint of the batch
// transaction and reports whether it produced a ledger row.
//
// A record the ledger refuses, such as one dated into a week no partition
// covers, used to fail the whole batch, which left the offset in place and the
// record redelivered until Kafka's retention discarded it: that is how the
// 2026-07-06 to 07-21 events were lost (TACK-336). The savepoint lets the
// failed statement be rolled back without aborting the batch, so the record
// is written to the dead-letter table in the same transaction that advances
// the offset. A chain conflict still fails the batch, because that is the
// retry the chain protocol depends on.
//
// A replayed record that lands deletes the dead-letter row it came from in
// the same transaction; one the ledger already holds does the same, since the
// event is accounted for either way.
func (c *Consumer) projectRecord(ctx context.Context, tx pgx.Tx, rec *kgo.Record) (projectedEvent, bool, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.savepoint_failed", slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("savepoint for offset=%d: %w", rec.Offset, err)
	}
	pe, perr := c.projectOne(ctx, savepoint, rec)
	if perr == nil {
		if err := savepoint.Commit(ctx); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.savepoint_release_failed", slog.String("err", err.Error()))
			return projectedEvent{}, false, fmt.Errorf("release savepoint for offset=%d: %w", rec.Offset, err)
		}
		return pe, true, resolveDeadLetter(ctx, tx, rec)
	}
	_ = savepoint.Rollback(ctx)
	switch {
	case errors.Is(perr, errAlreadyProjected):
		return projectedEvent{}, false, resolveDeadLetter(ctx, tx, rec)
	case isRetryableChainErr(perr):
		return projectedEvent{}, false, perr
	default:
		// Malformed payloads and refused inserts alike: the record is kept,
		// named, and counted in the dead-letter table, and the batch goes on.
		if !errors.Is(perr, errMalformedPayload) {
			slog.ErrorContext(ctx, "audit.consumer.project_record_failed",
				slog.String("err", perr.Error()),
				slog.Int64("offset", rec.Offset),
			)
		}
		return projectedEvent{}, false, deadLetterRecord(ctx, tx, rec, perr)
	}
}

// writeClickHouseBatch projects a committed batch into ClickHouse. The OLAP
// write is best effort, so a ClickHouse outage never blocks chain advancement.
func (c *Consumer) writeClickHouseBatch(ctx context.Context, projected []projectedEvent) {
	if c.ch == nil || len(projected) == 0 {
		return
	}
	// Best effort: insertOLAPBatch logs its own failure at WARN, and the
	// canonical Yugabyte write already committed, so a ClickHouse error never
	// blocks chain advancement (TACK-317).
	_ = insertOLAPBatch(ctx, c.ch, projected)
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
	Outcome    Outcome
	Error      []byte
	Extra      []byte
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
	ErrorJSON   []byte
	ExtraJSON   []byte
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
	errorJSON, err := json.Marshal(ev.Error)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.marshal_error_failed", slog.String("err", err.Error()))
		return preparedEvent{}, fmt.Errorf("marshal event error: %w", err)
	}
	extraJSON := []byte("null")
	if len(ev.Extra) != 0 {
		extraJSON = ev.Extra
	}
	return preparedEvent{
		Event:       ev,
		EventID:     eventID,
		Shard:       shard,
		ContextJSON: contextJSON,
		DeltaJSON:   deltaJSON,
		ErrorJSON:   errorJSON,
		ExtraJSON:   extraJSON,
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
		Outcome:    p.Event.Outcome,
		Error:      p.ErrorJSON,
		Extra:      p.ExtraJSON,
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
	Version     int16
}

// hashRowForEvent computes the per-event chain hash. Both the consumer and
// YBRecorder reach it through appendChainRow, so the chain is byte-identical
// regardless of which writer is active.
func hashRowForEvent(in rowHashInput) ([]byte, error) {
	// Version 3 hashes event_time at the microsecond precision the
	// TIMESTAMPTZ column stores, so offline verification can recompute the
	// hash from the read-back row in one try. Versions 1 and 2 hashed the
	// producer's nanosecond timestamp, which the database never stored, so
	// verification recovers their rows by trying every lost remainder
	// (TACK-445).
	eventTime := in.Event.OccurredAt.UTC().Format(time.RFC3339Nano)
	if in.Version == auditHashVersion3 {
		eventTime = in.Event.OccurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	payload := rowHashPayloadV1{
		OrgID:       in.Event.Context.OrgID,
		Shard:       in.Shard,
		Seq:         in.Seq,
		EventID:     in.EventID,
		EventTime:   eventTime,
		ActorID:     in.Event.Actor.ID,
		ActorKind:   in.Event.Actor.Type,
		Action:      in.Event.Verb,
		EntityKind:  in.Event.Entity.Type,
		EntityID:    in.Event.Entity.ID,
		PIIRef:      in.PIIRef,
		Context:     json.RawMessage(in.ContextJSON),
		Delta:       json.RawMessage(in.DeltaJSON),
		Idempotency: in.Event.IdempotencyKey,
	}
	payloadV2 := rowHashPayloadV2{
		rowHashPayloadV1: payload,
		Outcome:          in.Event.Outcome,
		Error:            in.Event.Error,
		Extra:            in.Event.Extra,
	}
	switch in.Version {
	case auditHashVersion1:
		return hashRow(in.LastHash, payload)
	case auditHashVersion2:
		return hashRow(in.LastHash, payloadV2)
	case auditHashVersion3:
		// The version is part of the hash from version 3 on. Versions 2 and 3
		// otherwise cover the same fields, and a version 3 row already carries
		// a whole-microsecond timestamp, so without this a forger could relabel
		// a version 3 row as version 2 and the legacy candidate search would
		// reproduce its hash on the first try.
		return hashRow(in.LastHash, rowHashPayloadV3{rowHashPayloadV2: payloadV2, HashVersion: in.Version})
	default:
		return nil, fmt.Errorf("unsupported audit hash version %d", in.Version)
	}
}

const (
	auditHashVersion1 int16 = 1
	auditHashVersion2 int16 = 2
	auditHashVersion3 int16 = 3
	// auditHashVersionCurrent is what appendChainRow both hashes with and
	// stores in the hash_version column, so the two cannot diverge.
	auditHashVersionCurrent = auditHashVersion3
)

type rowHashPayloadV1 struct {
	OrgID       uuid.UUID       `json:"org_id"`
	Shard       int16           `json:"shard"`
	Seq         int64           `json:"seq"`
	EventID     uuid.UUID       `json:"event_id"`
	EventTime   string          `json:"event_time"`
	ActorID     uuid.UUID       `json:"actor_id"`
	ActorKind   ActorType       `json:"actor_kind"`
	Action      string          `json:"action"`
	EntityKind  string          `json:"entity_kind"`
	EntityID    uuid.UUID       `json:"entity_id"`
	PIIRef      uuid.UUID       `json:"pii_ref"`
	Context     json.RawMessage `json:"context"`
	Delta       json.RawMessage `json:"delta"`
	Idempotency string          `json:"idempotency"`
}

type rowHashPayloadV2 struct {
	rowHashPayloadV1
	Outcome Outcome         `json:"outcome"`
	Error   *EventError     `json:"error"`
	Extra   json.RawMessage `json:"extra"`
}

type rowHashPayloadV3 struct {
	rowHashPayloadV2
	HashVersion int16 `json:"hash_version"`
}

// piiRefArg returns the value to bind for audit.events.pii_ref. The pgx
// adapter treats a typed nil interface as NULL; uuid.Nil written literally
// would produce a non-NULL all-zeros UUID, so we route through pgtype.
func piiRefArg(ref uuid.UUID) pgtype.UUID {
	if ref == uuid.Nil {
		return pgtype.UUID{Bytes: [16]byte{}, Valid: false}
	}
	return pgtype.UUID{Bytes: ref, Valid: true}
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
		    outcome         String,
		    error           String,
		    extra           String,
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
	if err := conn.Exec(ctx, `ALTER TABLE audit.events_olap ADD COLUMN IF NOT EXISTS outcome String AFTER action`); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_outcome_column_failed", slog.String("err", err.Error()))
		return fmt.Errorf("add outcome column: %w", err)
	}
	if err := conn.Exec(ctx, `ALTER TABLE audit.events_olap ADD COLUMN IF NOT EXISTS error String AFTER outcome`); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_error_column_failed", slog.String("err", err.Error()))
		return fmt.Errorf("add error column: %w", err)
	}
	if err := conn.Exec(ctx, `ALTER TABLE audit.events_olap ADD COLUMN IF NOT EXISTS extra String AFTER error`); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.clickhouse_extra_column_failed", slog.String("err", err.Error()))
		return fmt.Errorf("add extra column: %w", err)
	}
	return nil
}
