package audit

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// WALRecorder buffers read-class audit events on disk and forwards them to
// an inner Recorder (typically YBRecorder) from a background drainer.
// State-change verbs bypass the WAL and go straight to the inner Recorder
// so that ledger writes commit synchronously with the operation they audit.
//
// Segments are append-only files under Dir, one per clock minute. Each
// Record call appends a length-prefixed JSON event and fsyncs before
// returning. A process crash loses zero events. Loss only occurs when the
// drainer falls more than maxLag behind, at which point new reads are
// dropped and an audit.dropped self-reference is generated.
type WALRecorder struct {
	dir   string
	inner Recorder

	// maxBytesPerSegment caps the size of any one segment file. When the
	// active segment exceeds the cap, Append rotates to a new segment even
	// if the minute hasn't ticked over.
	maxBytesPerSegment int64

	// maxLag is the largest acceptable drainer backlog. When exceeded the
	// recorder drops further reads until the drainer catches up.
	maxLag time.Duration

	// drainInterval governs how often the drainer wakes when there's no
	// pending work. A pending segment is drained immediately on Append.
	drainInterval time.Duration

	mu      sync.Mutex
	current *segment

	dropped atomic.Int64
	stop    chan struct{}
	stopped chan struct{}
}

type segment struct {
	path string
	f    *os.File
	w    *bufio.Writer
	size int64
}

// WALConfig collects the knobs WALRecorder cares about. Zero values use
// production-friendly defaults.
type WALConfig struct {
	Dir                string
	MaxBytesPerSegment int64
	MaxLag             time.Duration
	DrainInterval      time.Duration
}

// NewWALRecorder opens (or creates) the WAL directory and starts the drainer.
// Caller must call Close to flush and stop the drainer cleanly.
func NewWALRecorder(ctx context.Context, inner Recorder, cfg WALConfig) (*WALRecorder, error) {
	if inner == nil {
		return nil, errors.New("audit: WAL inner Recorder required")
	}
	if cfg.Dir == "" {
		return nil, errors.New("audit: WALConfig.Dir required")
	}
	if cfg.MaxBytesPerSegment <= 0 {
		cfg.MaxBytesPerSegment = 64 << 20 // 64 MiB
	}
	if cfg.MaxLag <= 0 {
		cfg.MaxLag = 30 * time.Minute
	}
	if cfg.DrainInterval <= 0 {
		cfg.DrainInterval = 250 * time.Millisecond
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit wal mkdir: %w", err)
	}
	w := &WALRecorder{
		dir:                cfg.Dir,
		inner:              inner,
		maxBytesPerSegment: cfg.MaxBytesPerSegment,
		maxLag:             cfg.MaxLag,
		drainInterval:      cfg.DrainInterval,
		stop:               make(chan struct{}),
		stopped:            make(chan struct{}),
	}
	go w.drainLoop(ctx)
	return w, nil
}

// Record routes by verb. State-change verbs commit synchronously through
// the inner Recorder. Read verbs append to the WAL, fsync, and return; the
// drainer ships them to the inner Recorder asynchronously.
func (w *WALRecorder) Record(ctx context.Context, ev Event) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if IsStateChange(Verb(ev.Verb)) {
		return w.inner.Record(ctx, ev)
	}
	if w.atOverflow() {
		w.dropped.Add(1)
		telemetry.IncAuditDropped(ev.Verb, "wal_overflow")
		telemetry.L(ctx).Warn("audit.wal.dropped",
			slog.String("verb", ev.Verb),
			slog.Int64("dropped_total", w.dropped.Load()),
		)
		return nil
	}
	return w.append(ctx, ev)
}

// Close flushes the active segment and stops the drainer. Idempotent.
// After the drainer stops, Close runs one final pass that drains every
// remaining segment, including the one that was just closed; otherwise the
// trailing events would sit on disk indefinitely.
func (w *WALRecorder) Close() error {
	select {
	case <-w.stop:
		return nil
	default:
		close(w.stop)
	}
	<-w.stopped
	w.mu.Lock()
	if w.current != nil {
		_ = w.current.flushAndClose()
		w.current = nil
	}
	w.mu.Unlock()
	w.drainOnce(context.Background())
	return nil
}

func (w *WALRecorder) append(ctx context.Context, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		telemetry.IncAuditDropped(ev.Verb, "wal_marshal")
		return fmt.Errorf("audit wal marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureSegmentLocked(); err != nil {
		return err
	}
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(payload)))
	if _, err := w.current.w.Write(lp[:]); err != nil {
		return fmt.Errorf("audit wal write len: %w", err)
	}
	if _, err := w.current.w.Write(payload); err != nil {
		return fmt.Errorf("audit wal write payload: %w", err)
	}
	if err := w.current.w.Flush(); err != nil {
		return fmt.Errorf("audit wal flush: %w", err)
	}
	if err := w.current.f.Sync(); err != nil {
		return fmt.Errorf("audit wal fsync: %w", err)
	}
	w.current.size += int64(4 + len(payload))
	telemetry.L(ctx).Debug("audit.wal.appended",
		slog.String("verb", ev.Verb),
		slog.Int("payload_bytes", len(payload)),
	)
	return nil
}

func (w *WALRecorder) ensureSegmentLocked() error {
	now := time.Now().UTC()
	want := segmentName(now)
	if w.current != nil {
		if filepath.Base(w.current.path) == want && w.current.size < w.maxBytesPerSegment {
			return nil
		}
		_ = w.current.flushAndClose()
	}
	path := filepath.Join(w.dir, want)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit wal open segment: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("audit wal stat: %w", err)
	}
	w.current = &segment{path: path, f: f, w: bufio.NewWriter(f), size: info.Size()}
	return nil
}

func (s *segment) flushAndClose() error {
	if s == nil {
		return nil
	}
	if s.w != nil {
		_ = s.w.Flush()
	}
	if s.f != nil {
		_ = s.f.Sync()
		return s.f.Close()
	}
	return nil
}

// segmentName returns the canonical filename for the segment that owns the
// given time. Lexicographic order matches chronological order.
func segmentName(t time.Time) string {
	return t.Format("20060102T150405") + ".wal"
}

// atOverflow returns true when the oldest unfinished segment is more than
// maxLag old. Drainer health is implied by the absence of stale segments.
func (w *WALRecorder) atOverflow() bool {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return false
	}
	cutoff := time.Now().UTC().Add(-w.maxLag)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".wal") {
			continue
		}
		ts, ok := parseSegmentTime(e.Name())
		if !ok {
			continue
		}
		if ts.Before(cutoff) {
			return true
		}
	}
	return false
}

func parseSegmentTime(name string) (time.Time, bool) {
	stem := strings.TrimSuffix(name, ".wal")
	t, err := time.Parse("20060102T150405", stem)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (w *WALRecorder) drainLoop(ctx context.Context) {
	defer close(w.stopped)
	t := time.NewTicker(w.drainInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			w.drainOnce(ctx)
			return
		case <-ctx.Done():
			w.drainOnce(ctx)
			return
		case <-t.C:
			w.drainOnce(ctx)
		}
	}
}

// drainOnce drains every closed segment older than the active one. The
// currently-writing segment is left alone so we don't race the appender.
func (w *WALRecorder) drainOnce(ctx context.Context) {
	ctx, span := telemetry.StartSpan(ctx, "audit.wal.drain_once",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.wal"))

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.wal.scan_failed", slog.String("err", err.Error()))
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	w.mu.Lock()
	activeName := ""
	if w.current != nil {
		activeName = filepath.Base(w.current.path)
	}
	w.mu.Unlock()
	span.SetAttributes(
		attribute.Int("audit.wal.segment_count", len(names)),
		attribute.String("audit.wal.active_segment", activeName),
	)
	for _, name := range names {
		if name == activeName {
			continue
		}
		w.drainSegment(ctx, filepath.Join(w.dir, name))
	}
}

func (w *WALRecorder) drainSegment(ctx context.Context, path string) {
	ctx, span := telemetry.StartSpan(ctx, "audit.wal.drain_segment",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("audit.wal.path", path)),
	)
	defer span.End()

	start := time.Now()
	f, err := os.Open(path)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.wal.open_failed",
			slog.String("path", path), slog.String("err", err.Error()),
		)
		return
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	var count int
	for {
		var lp [4]byte
		if _, err := io.ReadFull(r, lp[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			telemetry.L(ctx).Error("audit.wal.read_len_failed",
				slog.String("path", path), slog.String("err", err.Error()),
			)
			return
		}
		n := binary.BigEndian.Uint32(lp[:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			telemetry.L(ctx).Error("audit.wal.read_payload_failed",
				slog.String("path", path), slog.String("err", err.Error()),
			)
			return
		}
		var ev Event
		if err := json.Unmarshal(buf, &ev); err != nil {
			span.RecordError(err)
			telemetry.L(ctx).Error("audit.wal.parse_failed",
				slog.String("path", path), slog.String("err", err.Error()),
			)
			continue
		}
		replayCtx := telemetry.WithRequestMetadata(ctx, ev.Context.RequestID)
		replayCtx = telemetry.WithTraceLogger(replayCtx, slog.String("replayed_trace_id", ev.Context.TraceID))
		if err := w.inner.Record(replayCtx, ev); err != nil {
			// Inner write failed; leave the segment in place so we retry.
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			telemetry.L(ctx).Error("audit.wal.replay_failed",
				slog.String("path", path),
				slog.String("verb", ev.Verb),
				slog.String("err", err.Error()),
			)
			return
		}
		count++
	}
	if err := os.Remove(path); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.wal.remove_failed",
			slog.String("path", path), slog.String("err", err.Error()),
		)
		return
	}
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(
		attribute.Int("audit.wal.events_replayed", count),
		attribute.Int64("audit.wal.duration_ms", time.Since(start).Milliseconds()),
	)
	telemetry.L(ctx).Info("audit.reconciler.flushed",
		slog.String("path", path),
		slog.Int("events", count),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
}
