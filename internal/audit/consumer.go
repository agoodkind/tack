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

// ConsumerConfig configures the audit-consumer connections and background work.
type ConsumerConfig struct {
	Brokers         []string
	Topic           string
	GroupID         string
	BatchSize       int
	PollInterval    time.Duration
	YugabyteDSN     string
	SigningKeyPath  string
	NotarizerPeriod time.Duration
	// SigningHost records the claimed host on each notarization (TACK-437).
	SigningHost string

	// LagWarnMessages sets the per-partition warning threshold. Zero uses 1000.
	LagWarnMessages int64

	// SummaryEvery sets the record count between debug summaries. Zero uses 100.
	SummaryEvery int

	// PartitionPeriod sets the partition maintenance interval. Zero uses 24h.
	PartitionPeriod time.Duration

	// TopicRetention limits how long the broker retains audit records during
	// consumer outages. Startup applies it to the topic. Zero uses one year.
	// Retention must cover recovery time to avoid losing pending events (TACK-336).
	TopicRetention time.Duration
}

// defaultTopicRetention bounds topic storage during consumer outages.
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
	// consecutiveFailures counts consecutive polls with failed batches.
	// Only the loop reads or writes it.
	consecutiveFailures int
	// assemblyAlertAt records the time of the last assembly alert check.
	assemblyAlertAt time.Time
}

// processedSummary accumulates batch counts for one debug summary per N
// records.
type processedSummary struct {
	count        int
	verbCounts   map[string]int
	commitOffset int64
}

// NewConsumer opens connections and prepares the embedded notarizer.
// The caller must call Start and Close.
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

	// Commit broker offsets explicitly after the ledger transaction commits.
	// Autocommit must not advance group offsets past unrecorded events.
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

// Start runs the poll loop and embedded notarizer until ctx ends or Close runs.
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

// Close stops the loop, finishes active work, and releases resources.
// Repeated calls do not repeat the stop signal.
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
				// An idle poll can end with a synthetic context error from
				// franz-go. Ignore cancellation and deadline errors here;
				// continue logging genuine partition errors.
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

// observeLag publishes each partition's lag and warns at the configured
// threshold. Lag is HighWatermark minus (the last consumed offset plus one).
// An empty fetch reports HighWatermark as lag, including during idle polls.
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

// recordSummary accumulates projected batches and logs every cfg.SummaryEvery
// records. It lists the five most frequent verbs, breaking ties alphabetically.
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

// verbFromRecord reads the JSON verb or returns _unparseable for invalid or
// missing verbs. Summaries count those records under that fallback value.
func verbFromRecord(rec *kgo.Record) string {
	var probe struct {
		Verb string `json:"verb"`
	}
	if err := json.Unmarshal(rec.Value, &probe); err != nil || probe.Verb == "" {
		return "_unparseable"
	}

	return probe.Verb
}

// topVerbs returns up to n verb=count strings ordered by descending count,
// then ascending verb.
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

// projectRecord projects one record inside a batch savepoint and reports
// whether it created a ledger row. A malformed record or refused insert rolls
// back to the savepoint and records a dead letter in the transaction that
// advances the offset. Chain conflicts, connection failures, and deployment
// faults still fail the batch for retry (TACK-336).
// A successful or already-projected replay deletes its original dead-letter
// row in the same transaction.
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
		// Preserve and count malformed payloads and refused inserts in the
		// dead-letter table before continuing the batch.
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

// projectOne writes one event's PII row, appends audit.events, and advances
// audit.chain_heads. Part and manifest messages instead commit in assembly
// storage with audit.consumer_offsets before the event is complete. Plain
// read events require evidence matching an empty target list; a mismatch
// records a dead letter.
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

// preparedEvent caches decoded fields and their JSON encodings for chain
// insertion.
type preparedEvent struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	ContextJSON []byte
	DeltaJSON   []byte
	PIIRef      uuid.UUID
}

// unmarshalRecord decodes an Event and prepares derived fields. Malformed
// payload errors tell the consumer to record a dead letter.
func unmarshalRecord(ctx context.Context, rec *kgo.Record) (preparedEvent, error) {
	var ev Event
	if err := json.Unmarshal(rec.Value, &ev); err != nil {
		slog.WarnContext(ctx, "audit.consumer.malformed_json", slog.String("err", err.Error()))
		return preparedEvent{}, fmt.Errorf("%w: %w", errMalformedPayload, err)
	}

	return prepareEvent(ctx, ev)
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

// advanceChain appends the event and advances its (org, shard) head with the
// shared compare-and-swap protocol. It returns errAlreadyProjected for a
// duplicate event ID and errChainConflict when another writer advances the
// head.
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

// rowHashInput contains the fields used to construct one chain hash payload.
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

// hashRowForEvent computes the chain hash for both consumer and YBRecorder
// appends through appendChainRow.
func hashRowForEvent(in rowHashInput) ([]byte, error) {
	// Version 3 hashes the microsecond precision stored by TIMESTAMPTZ.
	// Versions 1 and 2 hashed producer nanoseconds that the database did not
	// store. Verification tries every lost remainder for those rows (TACK-445).
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
		// Hash the version to prevent relabeling a version 3 row as version 2.
		// Their other fields and whole-microsecond timestamps can match.
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
	// auditHashVersionCurrent sets both the hash payload version and the stored
	// hash_version column in appendChainRow.
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

// piiRefArg binds SQL NULL for a nil reference and the UUID otherwise.
// A literal [uuid.Nil] would bind an all-zero UUID rather than SQL NULL.
func piiRefArg(ref uuid.UUID) pgtype.UUID {
	if ref == uuid.Nil {
		return pgtype.UUID{Bytes: [16]byte{}, Valid: false}
	}

	return pgtype.UUID{Bytes: ref, Valid: true}
}
