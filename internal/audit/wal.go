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
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// Disk-pressure defaults. The drainer compares free bytes against
// max(diskPressureMinBytes, diskPressurePercent * total) and emits a Warn
// slog when free space falls below that threshold. The signal is purely
// observational: it never causes Record to fail or drop events.
const (
	diskPressureMinBytes = int64(100 << 20) // 100 MiB
	diskPressurePercent  = 0.05             // 5%
)

// WALRecorder buffers read-class audit events on disk and forwards them to
// an inner Recorder (typically YBRecorder) from a background drainer.
// State-change verbs bypass the WAL and go straight to the inner Recorder
// so that ledger writes commit synchronously with the operation they audit.
//
// Segments are append-only files under Dir, one per clock minute. Each
// Record call appends a length-prefixed JSON event and fsyncs before
// returning. A process crash loses zero events. The on-disk fsync is the
// compliance commit. The WAL only fails a Record when the local disk write
// itself fails (for example [syscall.ENOSPC] or an I/O error). A backlog
// in the drainer is queueing, not failure, and is never cause to drop or
// reject an event.
type WALRecorder struct {
	dir   string
	inner Recorder

	// maxBytesPerSegment caps the size of any one segment file. When the
	// active segment exceeds the cap, Append rotates to a new segment even
	// if the minute hasn't ticked over.
	maxBytesPerSegment int64

	// drainInterval governs how often the drainer wakes when there's no
	// pending work. A pending segment is drained immediately on Append.
	drainInterval time.Duration

	// idleRotateAfter is how long an active segment may sit idle before the
	// drainer force-rotates it. Defaults to 2 * drainInterval.
	idleRotateAfter time.Duration

	// Backlog signals, updated atomically by drainOnce. Exposed via
	// BacklogSignal for telemetry only. Never consulted by Record.
	unflushedSegments       atomic.Int32
	oldestUnflushedUnixNano atomic.Int64
	lastDrainSuccessUnix    atomic.Int64

	// Counters for telemetry, read by the metrics package via WALStats.
	idleRotationsTotal atomic.Int64
	writeErrorsTotal   atomic.Int64

	// Disk-free gauge populated by drainOnce via syscall.Statfs.
	diskFreeBytes atomic.Int64

	// dropped counts events discarded for unrecoverable encoding errors
	// (JSON marshal failure). Disk-write failures are not counted here;
	// they propagate to the caller as errors.
	dropped atomic.Int64

	mu      sync.Mutex
	current *segment

	// statfs is the filesystem free-space probe. Production wires
	// [syscall.Statfs]; tests inject a fake to exercise the disk-pressure
	// path without depending on real free space. Reads and writes are
	// gated by statfsMu so a test can swap the probe atomically against
	// a drainer goroutine that is already running.
	statfsMu sync.RWMutex
	statfs   func(path string, buf *syscall.Statfs_t) error

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
	DrainInterval      time.Duration

	// MaxLag is retained for callers that pass it today; it is ignored by
	// the new backlog signal logic. Callers should migrate to MaxBacklogAge.
	MaxLag time.Duration

	// MaxBacklogSegments and MaxBacklogAge feed observability only. They
	// no longer drive any drop or backpressure decision; they exist so the
	// BacklogSignal telemetry can flag operators when the drainer is behind.
	MaxBacklogSegments int32
	MaxBacklogAge      time.Duration

	IdleRotateAfter time.Duration
}

// WALStats exposes atomic counters and gauges from a WALRecorder so the
// telemetry package can wire them into expvar without importing the audit
// package in a way that would create a cycle.
type WALStats struct {
	UnflushedSegments      func() int64
	OldestUnflushedAgeSecs func() float64
	LastDrainSuccessUnix   func() int64
	IdleRotationsTotal     func() int64
	WriteErrorsTotal       func() int64
	DiskFreeBytes          func() int64
}

// Stats returns live-reading closures over the WALRecorder's atomics.
// The returned struct is safe to call from any goroutine.
func (w *WALRecorder) Stats() WALStats {
	return WALStats{
		UnflushedSegments: func() int64 {
			return int64(w.unflushedSegments.Load())
		},
		OldestUnflushedAgeSecs: func() float64 {
			nano := w.oldestUnflushedUnixNano.Load()
			if nano == 0 {
				return 0
			}
			age := time.Now().UnixNano() - nano
			age = max(age, 0)
			return float64(age) / float64(time.Second)
		},
		LastDrainSuccessUnix: func() int64 {
			return w.lastDrainSuccessUnix.Load()
		},
		IdleRotationsTotal: func() int64 {
			return w.idleRotationsTotal.Load()
		},
		WriteErrorsTotal: func() int64 {
			return w.writeErrorsTotal.Load()
		},
		DiskFreeBytes: func() int64 {
			return w.diskFreeBytes.Load()
		},
	}
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
	if cfg.DrainInterval <= 0 {
		cfg.DrainInterval = 250 * time.Millisecond
	}
	if cfg.MaxBacklogSegments <= 0 {
		cfg.MaxBacklogSegments = 64
	}
	if cfg.MaxBacklogAge <= 0 {
		cfg.MaxBacklogAge = 10 * time.Minute
	}
	if cfg.IdleRotateAfter <= 0 {
		cfg.IdleRotateAfter = 2 * cfg.DrainInterval
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit wal mkdir: %w", err)
	}
	w := &WALRecorder{
		dir:                     cfg.Dir,
		inner:                   inner,
		maxBytesPerSegment:      cfg.MaxBytesPerSegment,
		drainInterval:           cfg.DrainInterval,
		idleRotateAfter:         cfg.IdleRotateAfter,
		unflushedSegments:       atomic.Int32{},
		oldestUnflushedUnixNano: atomic.Int64{},
		lastDrainSuccessUnix:    atomic.Int64{},
		idleRotationsTotal:      atomic.Int64{},
		writeErrorsTotal:        atomic.Int64{},
		diskFreeBytes:           atomic.Int64{},
		dropped:                 atomic.Int64{},
		mu:                      sync.Mutex{},
		current:                 nil,
		statfsMu:                sync.RWMutex{},
		statfs:                  syscall.Statfs,
		stop:                    make(chan struct{}),
		stopped:                 make(chan struct{}),
	}
	go w.drainLoop(ctx)
	return w, nil
}

// Record routes by verb. State-change verbs commit synchronously through
// the inner Recorder. Read verbs append to the WAL, fsync, and return; the
// drainer ships them to the inner Recorder asynchronously.
//
// Read events ALWAYS go to append regardless of backlog state. The fsync to
// the local segment file is the compliance commit. Record only fails when
// the underlying disk write fails (for example [syscall.ENOSPC] or an I/O
// error); a behind drainer is queueing, not failure.
func (w *WALRecorder) Record(ctx context.Context, ev Event) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if IsStateChange(Verb(ev.Verb)) {
		return w.inner.Record(ctx, ev)
	}
	return w.append(ctx, ev)
}

// Close flushes the active segment and stops the drainer. Idempotent.
// After the drainer stops, Close runs final drain passes that flush every
// remaining segment, including the one that was just closed; otherwise the
// trailing events would sit on disk indefinitely. Multiple passes are
// required because each pass may force-rotate an idle segment whose
// follow-up drain is queued to the NEXT pass.
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
	ctx := context.Background()
	// Drain repeatedly until the directory is empty or a small bound is
	// reached. The second pass picks up segments that were force-rotated
	// during the first pass; in tests with very short idleRotateAfter this
	// can recur a few times.
	for range 8 {
		w.drainOnce(ctx)
		if w.unflushedSegments.Load() == 0 {
			return nil
		}
	}
	return nil
}

func (w *WALRecorder) append(ctx context.Context, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		w.dropped.Add(1)
		telemetry.IncAuditDropped(ev.Verb, "wal_marshal")
		return fmt.Errorf("audit wal marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureSegmentLocked(); err != nil {
		w.recordWriteError(ctx, ev.Verb, "open_segment", err)
		return err
	}
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(payload)))
	if _, err := w.current.w.Write(lp[:]); err != nil {
		w.recordWriteError(ctx, ev.Verb, "write_len", err)
		return fmt.Errorf("audit wal write len: %w", err)
	}
	if _, err := w.current.w.Write(payload); err != nil {
		w.recordWriteError(ctx, ev.Verb, "write_payload", err)
		return fmt.Errorf("audit wal write payload: %w", err)
	}
	if err := w.current.w.Flush(); err != nil {
		w.recordWriteError(ctx, ev.Verb, "flush", err)
		return fmt.Errorf("audit wal flush: %w", err)
	}
	if err := w.current.f.Sync(); err != nil {
		w.recordWriteError(ctx, ev.Verb, "fsync", err)
		return fmt.Errorf("audit wal fsync: %w", err)
	}
	w.current.size += int64(4 + len(payload))
	telemetry.L(ctx).Debug("audit.wal.appended",
		slog.String("verb", ev.Verb),
		slog.Int("payload_bytes", len(payload)),
	)
	return nil
}

// recordWriteError increments the write-error counter and emits a single
// Error slog line. ENOSPC is logged at the same level; the operator alert
// fires on the metric, not the log level.
func (w *WALRecorder) recordWriteError(ctx context.Context, verb, stage string, err error) {
	w.writeErrorsTotal.Add(1)
	enospc := errors.Is(err, syscall.ENOSPC)
	telemetry.L(ctx).Error("audit.wal.write_failed",
		slog.String("verb", verb),
		slog.String("stage", stage),
		slog.String("err", err.Error()),
		slog.Bool("enospc", enospc),
	)
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

// rotateActiveLocked flushes and closes the active segment, renames the file
// to a unique sealed name so that subsequent Records cannot reopen and append
// to the same path while the drainer is reading and unlinking it, and returns
// the sealed path so the caller can drain it. Returns empty string when there
// is no active segment or when sealing failed (in which case the file is
// left in place and a future drain pass will pick it up safely after the
// per-second canonical name has aged out of the producer's active window).
// Must be called with w.mu held.
func (w *WALRecorder) rotateActiveLocked() string {
	if w.current == nil {
		return ""
	}
	path := w.current.path
	_ = w.current.flushAndClose()
	w.current = nil
	sealedPath := sealedSegmentPath(path)
	err := os.Rename(path, sealedPath)
	if err != nil {
		// Rename failed; the original file still exists at `path`. We
		// cannot return that path because a fresh Record on the same
		// canonical second would reopen and append to it while the
		// drainer is reading and unlinking it. Leave the file alone and
		// rely on a later drainOnce pass to pick it up once the active
		// segment timestamp has advanced past this second.
		slog.Warn("audit.wal.rotate_seal_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)
		return ""
	}
	slog.Debug("audit.wal.rotate_sealed",
		slog.String("from", path),
		slog.String("to", sealedPath),
	)
	return sealedPath
}

// sealedSegmentPath returns a unique sibling path for an active segment that
// is being force-rotated. The new name keeps the .wal suffix (so the scanner
// still picks it up) and adds a nanosecond-precision tag so it sorts after
// the original second-granularity name and never collides with a future
// per-second active segment.
func sealedSegmentPath(activePath string) string {
	stem := strings.TrimSuffix(filepath.Base(activePath), ".wal")
	nanos := time.Now().UTC().Format(".000000000")
	sealed := stem + nanos + ".wal"
	return filepath.Join(filepath.Dir(activePath), sealed)
}

// segmentName returns the canonical filename for the segment that owns the
// given time. Lexicographic order matches chronological order.
func segmentName(t time.Time) string {
	return t.Format("20060102T150405") + ".wal"
}

// BacklogSignal returns true when the cached backlog atomics indicate the
// drainer is behind one of the operator-visible thresholds. It is a pure
// atomic load with no filesystem I/O.
//
// This is observability only. Record never consults this value; the WAL
// always accepts read events as long as the local disk write succeeds.
func (w *WALRecorder) BacklogSignal(maxBacklogSegments int32, maxBacklogAge time.Duration) bool {
	if w.unflushedSegments.Load() > maxBacklogSegments {
		return true
	}
	oldest := w.oldestUnflushedUnixNano.Load()
	if oldest == 0 {
		return false
	}
	age := time.Now().UnixNano() - oldest
	if age < 0 {
		// Clock skew: treat negative age as zero (not old enough to trigger).
		return false
	}
	return age > int64(maxBacklogAge)
}

func parseSegmentTime(name string) (time.Time, bool) {
	stem := strings.TrimSuffix(name, ".wal")
	if t, err := time.Parse("20060102T150405", stem); err == nil {
		return t, true
	}
	if t, err := time.Parse("20060102T150405.000000000", stem); err == nil {
		return t, true
	}
	return time.Time{}, false
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

// drainOnce drains all closed segments and, when the active segment has been
// idle for longer than idleRotateAfter, force-rotates it so it too gets
// drained in the same pass.
//
// At the start of each call the non-active segment list is scanned to update
// the backlog signal atomics consulted by BacklogSignal. The drainer also
// samples the WAL filesystem's free bytes via [syscall.Statfs] and emits a
// Warn slog when free space drops below the disk-pressure threshold.
func (w *WALRecorder) drainOnce(ctx context.Context) {
	ctx, span := telemetry.StartSpan(ctx, "audit.wal.drain_once",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.wal"))

	names, activeName, ok := w.scanSegments(ctx, span)
	if !ok {
		return
	}

	w.updateBacklogSignals(names, activeName)
	w.sampleDiskFree(ctx)

	span.SetAttributes(
		attribute.Int("audit.wal.segment_count", len(names)),
		attribute.String("audit.wal.active_segment", activeName),
	)

	names, activeName = w.maybeIdleRotate(ctx, names, activeName)

	for _, name := range names {
		if name == activeName {
			continue
		}
		w.drainSegment(ctx, filepath.Join(w.dir, name))
	}

	w.lastDrainSuccessUnix.Store(time.Now().Unix())
}

// safeMulToInt64 multiplies two uint64 values and returns the result as int64,
// clamping to [math.MaxInt64] on overflow. Used to convert raw Statfs block
// counts into byte totals without tripping G115.
func safeMulToInt64(a, b uint64) int64 {
	const maxInt64 = uint64(1<<63 - 1)
	if a == 0 || b == 0 {
		return 0
	}
	if a > maxInt64/b {
		return math.MaxInt64
	}
	product := a * b
	if product > maxInt64 {
		return math.MaxInt64
	}
	return int64(product)
}

// statfsBlockSize returns the filesystem block size from a [syscall.Statfs_t]
// as a uint64. The Bsize field has different concrete types across platforms
// (int64 on Linux, uint32 on Darwin); reflect lets a single source file
// handle both without a per-platform helper. Negative values are clamped to
// zero so the multiplication can never overflow into a negative product.
func statfsBlockSize(st *syscall.Statfs_t) uint64 {
	v := reflect.ValueOf(st).Elem().FieldByName("Bsize")
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n < 0 {
			return 0
		}
		return uint64(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Invalid, reflect.Bool, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return 0
	default:
		return 0
	}
}

// sampleDiskFree updates the diskFreeBytes gauge and emits a Warn slog when
// the WAL filesystem's free space drops below max(5% of total, 100 MiB).
// The signal is observability only; it never affects Record.
func (w *WALRecorder) sampleDiskFree(ctx context.Context) {
	var st syscall.Statfs_t
	w.statfsMu.RLock()
	statfs := w.statfs
	w.statfsMu.RUnlock()
	if err := statfs(w.dir, &st); err != nil {
		telemetry.L(ctx).Warn("audit.wal.statfs_failed",
			slog.String("dir", w.dir),
			slog.String("err", err.Error()),
		)
		return
	}
	bsize := statfsBlockSize(&st)
	freeBytes := safeMulToInt64(st.Bavail, bsize)
	totalBytes := safeMulToInt64(st.Blocks, bsize)
	w.diskFreeBytes.Store(freeBytes)
	threshold := max(int64(float64(totalBytes)*diskPressurePercent), diskPressureMinBytes)
	if freeBytes < threshold {
		telemetry.L(ctx).Warn("audit.wal.disk_pressure",
			slog.String("dir", w.dir),
			slog.Int64("free_bytes", freeBytes),
			slog.Int64("total_bytes", totalBytes),
			slog.Int64("threshold_bytes", threshold),
		)
	}
}

// scanSegments reads the WAL directory and returns sorted segment names plus
// the active segment name. Returns ok=false on read error.
func (w *WALRecorder) scanSegments(ctx context.Context, span trace.Span) (names []string, activeName string, ok bool) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.wal.scan_failed", slog.String("err", err.Error()))
		return nil, "", false
	}
	names = make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	w.mu.Lock()
	if w.current != nil {
		activeName = filepath.Base(w.current.path)
	}
	w.mu.Unlock()
	return names, activeName, true
}

// updateBacklogSignals scans the non-active segment list and stores the count
// and oldest timestamp in the shared atomics.
func (w *WALRecorder) updateBacklogSignals(names []string, activeName string) {
	var count int32
	var oldestNano int64
	for _, name := range names {
		if name == activeName {
			continue
		}
		count++
		if ts, ok := parseSegmentTime(name); ok {
			nano := ts.UnixNano()
			if oldestNano == 0 || nano < oldestNano {
				oldestNano = nano
			}
		}
	}
	w.unflushedSegments.Store(count)
	w.oldestUnflushedUnixNano.Store(oldestNano)
}

// maybeIdleRotate checks whether the active segment is idle and, if so,
// rotates it and adds the rotated segment to the names list for draining.
// Returns the updated names slice and the new activeName (empty string if
// rotation occurred).
func (w *WALRecorder) maybeIdleRotate(ctx context.Context, names []string, activeName string) ([]string, string) {
	if activeName == "" {
		return names, activeName
	}
	ts, ok := parseSegmentTime(activeName)
	if !ok {
		return names, activeName
	}
	idle := time.Since(ts)
	if idle <= w.idleRotateAfter {
		return names, activeName
	}
	w.mu.Lock()
	rotatedPath := w.rotateActiveLocked()
	w.mu.Unlock()
	if rotatedPath == "" {
		return names, ""
	}
	w.idleRotationsTotal.Add(1)
	telemetry.L(ctx).Info("audit.wal.idle_rotated",
		slog.String("path", rotatedPath),
		slog.Int64("idle_ms", idle.Milliseconds()),
	)
	rotatedName := filepath.Base(rotatedPath)
	// Replace the now-renamed active name with the sealed name in the
	// drain list so the iterator below opens the path that actually
	// exists on disk. If the active name is not present (already drained
	// by a prior pass), append the sealed name.
	idx := slices.Index(names, activeName)
	if idx >= 0 {
		names[idx] = rotatedName
	} else if !slices.Contains(names, rotatedName) {
		names = append(names, rotatedName)
	}
	sort.Strings(names)
	return names, ""
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
