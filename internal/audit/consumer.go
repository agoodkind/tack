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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// ConsumerConfig supplies audit-consumer connection and processing settings.
// NewConsumer applies defaults for omitted settings.
type ConsumerConfig struct {
	Brokers         []string
	Topic           string
	GroupID         string
	BatchSize       int
	PollInterval    time.Duration
	YugabyteDSN     string
	SigningKeyPath  string
	NotarizerPeriod time.Duration
	// SigningHost identifies the host on every notarization that the embedded
	// notarizer writes (TACK-437).
	SigningHost string

	// LagWarnMessages sets the per-partition threshold for consumer.lag.high
	// warnings on each poll. The consumer warns at or above the threshold.
	// A nonpositive value uses 1000, the Wave 1 operator default.
	LagWarnMessages int64

	// SummaryEvery sets the record count between consumer.processed debug
	// summaries. A nonpositive value uses 100.
	SummaryEvery int

	// PartitionPeriod sets the pg_partman maintenance interval for audit.events.
	// A nonpositive value uses 24h.
	PartitionPeriod time.Duration

	// TopicRetention sets how long the broker retains audit records, including
	// records that the consumer has not processed. The consumer requests
	// retention at startup. A nonpositive value uses one year. The seven-day
	// broker default discarded fifteen days of events during a consumer outage
	// (TACK-336).
	TopicRetention time.Duration
}

// defaultTopicRetention retains records for one year to allow recovery from
// an extended consumer outage. It limits retention time rather than disk
// usage.
const defaultTopicRetention = 365 * 24 * time.Hour

// Consumer records Kafka audit events in YugabyteDB and runs the embedded
// notarizer. It writes audit.events and audit.chain_heads.
type Consumer struct {
	cfg     ConsumerConfig
	kclient *kgo.Client
	ybpool  *pgxpool.Pool

	notarizer  *Notarizer
	partitions *PartitionManager

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	summary processedSummary
	// consecutiveFailures counts failed polls with records since the last
	// successful poll with records. Only the consumer loop reads and writes it.
	consecutiveFailures int
	// assemblyAlertAt records the time of the last assembly alert check.
	assemblyAlertAt time.Time
}

// processedSummary accumulates batch counts for consumer.processed debug
// summaries after the configured record count.
type processedSummary struct {
	count        int
	verbCounts   map[string]int
	commitOffset int64
}

// NewConsumer prepares a [Consumer] for [Consumer.Start].
//
// It connects to YugabyteDB and Kafka and prepares the embedded notarizer
// when SigningKeyPath supplies a key.
//
// Manage the returned consumer in this order:
//
//  1. Call [Consumer.Start] to run the consumer.
//  2. Call [Consumer.Close] to release its resources.
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
		slog.ErrorContext(
			ctx,
			"audit.consumer.yugabyte_pool_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit consumer yugabyte pool: %w", err)
	}

	if err := pingYugabyteUntilReady(ctx, ybpool); err != nil {
		ybpool.Close()
		return nil, err
	}

	// The loop commits Kafka offsets after the batch transaction commits.
	// Automatic commits remain disabled. The earlier loop marked records for
	// automatic commits without enabling them and never committed an offset.
	// Every restart reread the topic from the start (TACK-336 review).
	kclient, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.GroupID),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.DisableAutoCommit(),
		kgo.FetchMaxWait(cfg.PollInterval),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.kafka_client_failed",
			slog.String("err", err.Error()),
		)
		ybpool.Close()

		return nil, fmt.Errorf("audit consumer kafka client: %w", err)
	}

	if err := ensureAuditTopic(ctx, kclient, cfg.Topic, cfg.TopicRetention); err != nil {
		kclient.Close()
		ybpool.Close()

		return nil, err
	}

	c := &Consumer{
		cfg:        cfg,
		kclient:    kclient,
		ybpool:     ybpool,
		notarizer:  nil,
		partitions: nil,
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
		once:       sync.Once{},
		summary: processedSummary{
			count:        0,
			verbCounts:   make(map[string]int),
			commitOffset: 0,
		},
		consecutiveFailures: 0,
		assemblyAlertAt:     time.Time{},
	}

	if cfg.SigningKeyPath != "" {
		n, nerr := NewNotarizer(
			ctx,
			cfg.YugabyteDSN,
			NotarizerConfig{
				SigningKeyPath: cfg.SigningKeyPath,
				SigningHost:    cfg.SigningHost,
				Period:         cfg.NotarizerPeriod,
			},
		)
		if nerr != nil {
			slog.ErrorContext(
				ctx,
				"audit.consumer.notarizer_failed",
				slog.String("err", nerr.Error()),
			)
			c.closeResources()

			return nil, fmt.Errorf("audit consumer notarizer: %w", nerr)
		}

		c.notarizer = n
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

// Start runs the consumer poll loop.
//
// It also starts the embedded notarizer and partition manager when present.
//
// Either condition stops the poll loop:
//
//   - ctx.Done() closes after cancellation or a deadline.
//   - The caller calls [Consumer.Close].
func (c *Consumer) Start(ctx context.Context) {
	if c.notarizer != nil {
		c.notarizer.Start(ctx)
	}

	if c.partitions != nil {
		c.partitions.Start(ctx)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(
					ctx,
					"audit.consumer.loop_panic",
					slog.Any("panic", r),
					slog.String("err", fmt.Sprintf("%v", r)),
				)
			}
		}()
		c.loop(ctx)
	}()
}

// Close drains in-flight work and releases every underlying resource.
//
// Only the first call signals the consumer loop to stop. Each call waits for
// the loop to stop before closing resources.
//
// Close releases resources in this order:
//
//  1. Close the embedded notarizer and partition manager when present.
//  2. Close the Kafka client and YugabyteDB pool.
func (c *Consumer) Close() error {
	c.once.Do(func() { close(c.stop) })
	<-c.stopped
	if c.notarizer != nil {
		_ = c.notarizer.Close()
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
				// An idle poll deadline produces a synthetic franz-go fetch error with an
				// empty topic and partition -1. Cancellation and deadline errors do not
				// indicate a partition failure. The loop skips them and logs the other
				// per-partition errors, including separately reported fatal errors.
				if errors.Is(fe.Err, context.DeadlineExceeded) ||
					errors.Is(fe.Err, context.Canceled) {
					continue
				}

				slog.ErrorContext(
					ctx,
					"audit.consumer.fetch_err",
					slog.String("topic", fe.Topic),
					slog.Int("partition", int(fe.Partition)),
					slog.String("err", fe.Err.Error()),
				)
			}
		}

		c.observeLag(ctx, fetches)

		hasRecords := fetches.NumRecords() > 0
		failed := false
		if hasRecords {
			failed = c.projectFetches(ctx, fetches)
		}

		c.completeAssemblies(ctx)
		c.alertWaitingAssemblies(ctx)
		if !hasRecords {
			continue
		}

		telemetry.ObserveAuditConsumerBatchLatency(sinceMs(pollStart))
		c.backOffAfterFailures(ctx, failed)
	}
}

// observeLag records lag for each partition in the fetch response. It
// subtracts the next offset after the last returned record from
// HighWatermark, or uses HighWatermark when the poll did not return records.
// It clamps negative lag to zero and logs consumer.lag.high at or above the
// configured threshold on every poll, including idle polls.
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
			slog.WarnContext(
				ctx,
				"consumer.lag.high",
				slog.String("topic", p.Topic),
				slog.Int("partition", int(p.Partition)),
				slog.Int64("lag", lag),
			)
		}
	})
}

// recordSummary adds the processed batch to the running summary. It logs
// consumer.processed after the count meets cfg.SummaryEvery. The verb
// summary includes the five most frequent verbs and orders equal counts
// alphabetically.
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

	telemetry.L(ctx).Debug(
		"consumer.processed",
		slog.Int("count", c.summary.count),
		slog.Any("verb_breakdown_top5", topVerbs(c.summary.verbCounts, 5)),
		slog.Int64("commit_offset", c.summary.commitOffset),
	)
	c.summary.count = 0
	c.summary.verbCounts = make(map[string]int)
}

// verbFromRecord reads the verb from the record's JSON value. Invalid JSON
// or an empty verb returns "_unparseable", which the summary still counts.
func verbFromRecord(rec *kgo.Record) string {
	var probe struct {
		Verb string `json:"verb"`
	}
	if err := json.Unmarshal(rec.Value, &probe); err != nil || probe.Verb == "" {
		return "_unparseable"
	}

	return probe.Verb
}

// topVerbs returns up to n "verb=count" strings for the log summary. It
// orders counts from highest to lowest and orders equal counts by verb
// alphabetically.
func topVerbs(counts map[string]int, n int) []string {
	type pair struct {
		verb  string
		count int
	}

	pairs := make([]pair, 0, len(counts))
	for v, c := range counts {
		pairs = append(pairs, pair{v, c})
	}

	sort.Slice(
		pairs,
		func(i, j int) bool {
			if pairs[i].count != pairs[j].count {
				return pairs[i].count > pairs[j].count
			}

			return pairs[i].verb < pairs[j].verb
		},
	)
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

func (c *Consumer) projectBatch(
	ctx context.Context,
	tp topicPartition,
	records []*kgo.Record,
) error {
	const maxChainRetries = 8

	for attempt := 1; ; attempt++ {
		err := c.projectBatchOnce(ctx, tp, records)
		if err == nil {
			return nil
		}

		if attempt < maxChainRetries && isRetryableChainErr(err) {
			slog.WarnContext(
				ctx,
				"audit.consumer.chain_retry",
				slog.String("topic", tp.Topic),
				slog.Int("partition", int(tp.Partition)),
				slog.Int("attempt", attempt),
			)
			continue
		}

		return err
	}
}

// projectBatchOnce saves ledger rows, token use times, and the consumer offset
// in one YugabyteDB transaction.
func (c *Consumer) projectBatchOnce(
	ctx context.Context,
	tp topicPartition,
	records []*kgo.Record,
) error {
	tx, err := c.ybpool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.begin_failed", slog.String("err", err.Error()))
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	projected := make([]committedAuthEvent, 0, len(records))
	for _, rec := range records {
		pe, landed, perr := c.projectRecord(ctx, tx, rec)
		if perr != nil {
			return perr
		}

		if landed {
			projected = append(projected, pe)
		}
	}

	if err := recordTokenUses(ctx, tx, projected); err != nil {
		return err
	}

	last := records[len(records)-1]
	_, err = tx.Exec(
		ctx,
		`
		INSERT INTO audit.consumer_offsets (consumer_group, topic, partition, "offset", updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (consumer_group, topic, partition) DO UPDATE
		    SET "offset"   = EXCLUDED."offset",
		        updated_at = EXCLUDED.updated_at
	`,
		c.cfg.GroupID,
		tp.Topic,
		tp.Partition,
		last.Offset+1,
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.offset_upsert_failed",
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("offset upsert: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// projectRecord processes one record in a savepoint of the batch transaction
// and reports whether it produced a ledger row.
//
// A ledger refusal, such as an event outside every weekly partition, used to
// fail the batch and prevent offset progress. Kafka repeatedly delivered the
// record until retention discarded it. This caused the 2026-07-06 to 07-21
// event loss (TACK-336). The savepoint permits rollback of the failed
// statement before the batch stores the record in the dead-letter table and
// advances its offset in the same transaction.
//
// Only record-specific failures qualify for the dead-letter table under
// isDeadLetterable. Chain conflicts, lost connections, and deployment faults
// fail the batch for retry. A successful replay deletes its dead-letter row
// in the same transaction. A replay of an event already in the ledger also
// deletes its dead-letter row.
func (c *Consumer) projectRecord(
	ctx context.Context,
	tx pgx.Tx,
	rec *kgo.Record,
) (committedAuthEvent, bool, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.savepoint_failed", slog.String("err", err.Error()))

		return committedAuthEvent{}, false, fmt.Errorf(
			"savepoint for offset=%d: %w",
			rec.Offset,
			err,
		)
	}

	pe, landed, perr := c.projectOne(ctx, savepoint, rec)
	if perr == nil {
		if err := savepoint.Commit(ctx); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.consumer.savepoint_release_failed",
				slog.String("err", err.Error()),
			)

			return committedAuthEvent{}, false, fmt.Errorf(
				"release savepoint for offset=%d: %w",
				rec.Offset,
				err,
			)
		}

		return pe, landed, resolveDeadLetter(ctx, tx, rec)
	}

	_ = savepoint.Rollback(ctx)
	switch {
	case errors.Is(perr, errAlreadyProjected):
		return committedAuthEvent{}, false, resolveDeadLetter(ctx, tx, rec)
	case isRetryableChainErr(perr):
		return committedAuthEvent{}, false, perr
	case isDeadLetterable(perr):
		// The dead-letter table preserves and identifies malformed payloads and
		// refused inserts. The consumer counts the record and continues the batch.
		telemetry.IncAuditConsumerDeadLettered()
		return committedAuthEvent{}, false, deadLetterRecord(ctx, tx, rec, perr)
	default:
		slog.ErrorContext(
			ctx,
			"audit.consumer.project_record_failed",
			slog.String("err", perr.Error()),
			slog.Int64("offset", rec.Offset),
		)

		return committedAuthEvent{}, false, fmt.Errorf(
			"project record offset=%d: %w",
			rec.Offset,
			perr,
		)
	}
}

var (
	errMalformedPayload = errors.New("malformed payload")
	errAlreadyProjected = errors.New("event already projected")
)

type committedAuthEvent struct {
	EventTime  time.Time
	Action     string
	APITokenID uuid.UUID
}

// projectOne first checks for part and manifest messages. It stores them in
// the batch transaction that advances audit.consumer_offsets. The consumer
// can commit broker offsets before the assembled event is complete.
//
// For a plain event, it writes personal data before appending audit.events
// and advancing audit.chain_heads with the shared chain helper.
//
// Require every plain read event to declare read evidence that matches an
// empty target list.
//
// When checkReadManifest returns an error, the consumer writes the record
// to the dead-letter table.
func (c *Consumer) projectOne(
	ctx context.Context,
	tx pgx.Tx,
	rec *kgo.Record,
) (committedAuthEvent, bool, error) {
	message, isTransport, err := decodeTransportMessage(rec.Value)
	if err != nil {
		return committedAuthEvent{}, false, rejectTransport(ctx, message.EventID, err.Error())
	}

	if isTransport {
		return committedAuthEvent{}, false, storeTransportMessage(ctx, tx, message, rec.Value)
	}

	prepared, err := unmarshalRecord(ctx, rec)
	if err != nil {
		return committedAuthEvent{}, false, err
	}

	if err := checkReadManifest(prepared.Event, nil); err != nil {
		return committedAuthEvent{}, false, rejectTransport(ctx, prepared.EventID, err.Error())
	}

	piiRef, err := writeEventPII(ctx, tx, prepared.Event)
	if err != nil {
		return committedAuthEvent{}, false, err
	}

	prepared.PIIRef = piiRef
	projected, err := c.advanceChain(ctx, tx, prepared)

	return projected, err == nil, err
}

// preparedEvent stores a decoded Kafka event before chain insertion. It
// caches JSON encodings for chain advancement.
type preparedEvent struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	ContextJSON []byte
	DeltaJSON   []byte
	PIIRef      uuid.UUID
}

// unmarshalRecord decodes the Kafka record into an [Event] and prepares its
// derived fields. JSON decoding errors wrap errMalformedPayload for the
// dead-letter table. Event preparation can also fail before insertion.
func unmarshalRecord(ctx context.Context, rec *kgo.Record) (preparedEvent, error) {
	var event Event
	if err := json.Unmarshal(rec.Value, &event); err != nil {
		slog.WarnContext(ctx, "audit.consumer.malformed_json", slog.String("err", err.Error()))
		return preparedEvent{}, fmt.Errorf("%w: %w", errMalformedPayload, err)
	}

	return prepareEvent(ctx, event)
}

// prepareEvent validates the decoded event, normalizes correlation fields,
// and computes its shard and JSON fields for ledger insertion.
// Both plain records and assembled events use this preparation.
func prepareEvent(ctx context.Context, ev Event) (preparedEvent, error) {
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
		slog.ErrorContext(
			ctx,
			"audit.consumer.marshal_context_failed",
			slog.String("err", err.Error()),
		)

		return preparedEvent{}, fmt.Errorf("marshal context: %w", err)
	}

	deltaJSON := []byte("null")
	if ev.Delta != nil {
		deltaJSON, err = json.Marshal(ev.Delta)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"audit.consumer.marshal_delta_failed",
				slog.String("err", err.Error()),
			)

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

// advanceChain appends the event to audit.events and updates its
// organization and shard head with the shared compare-and-swap helper.
// That helper rejects concurrent updates to the same head to prevent forks.
// It returns errAlreadyProjected for a repeated event identity and
// errChainConflict when another writer changes the head.
func (c *Consumer) advanceChain(
	ctx context.Context,
	tx pgx.Tx,
	p preparedEvent,
) (committedAuthEvent, error) {
	_, err := appendChainRow(
		ctx,
		tx,
		chainAppendInput{
			Event:       p.Event,
			EventID:     p.EventID,
			Shard:       p.Shard,
			PIIRef:      p.PIIRef,
			ContextJSON: p.ContextJSON,
			DeltaJSON:   p.DeltaJSON,
		},
	)
	if err != nil {
		return committedAuthEvent{}, err
	}

	return committedAuthEvent{
		EventTime:  p.Event.OccurredAt.UTC(),
		Action:     p.Event.Verb,
		APITokenID: p.Event.Context.APITokenID,
	}, nil
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

	if _, err := tx.Exec(
		ctx,
		`
		INSERT INTO audit.pii (pii_ref, payload, redacted)
		VALUES ($1, $2, false)
		ON CONFLICT DO NOTHING
	`,
		piiRef,
		piiPayload,
	); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.pii_insert_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("pii insert: %w", err)
	}

	return piiRef, nil
}

// rowHashInput provides the fields for one event's hash-chain payload.
// The struct replaces an inline map[string]any at the call site to satisfy
// lint requirements.
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

// hashRowForEvent computes the event's chain hash. The consumer and
// [YBRecorder] both use hashRowForEvent through appendChainRow with the same
// hash encoding.
func hashRowForEvent(in rowHashInput) ([]byte, error) {
	// Version 3 hashes event_time at the microsecond precision that TIMESTAMPTZ
	// stores. Offline verification can recompute the timestamp input from the
	// stored row. Versions 1 and 2 hashed the producer's nanosecond timestamp.
	// The database omitted that precision, and verification tries each lost
	// remainder for those versions (TACK-445).
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
		// Version 3 includes the version in the hash. Versions 2 and 3 otherwise
		// hash the same fields, and version 3 uses whole-microsecond timestamps.
		// Without the version field, relabeling a version 3 row as version 2 would
		// let the legacy candidate search reproduce its hash on the first attempt.
		return hashRow(
			in.LastHash,
			rowHashPayloadV3{rowHashPayloadV2: payloadV2, HashVersion: in.Version},
		)
	default:
		return nil, fmt.Errorf("unsupported audit hash version %d", in.Version)
	}
}

const (
	auditHashVersion1 int16 = 1
	auditHashVersion2 int16 = 2
	auditHashVersion3 int16 = 3
	// appendChainRow uses auditHashVersionCurrent for both the hash input and
	// the hash_version column.
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

// piiRefArg returns the audit.events.pii_ref parameter as a [pgtype.UUID].
// An invalid pgtype.UUID represents SQL NULL. A literal [uuid.Nil] would
// store a non-NULL zero UUID.
func piiRefArg(ref uuid.UUID) pgtype.UUID {
	if ref == uuid.Nil {
		return pgtype.UUID{Bytes: [16]byte{}, Valid: false}
	}

	return pgtype.UUID{Bytes: ref, Valid: true}
}
