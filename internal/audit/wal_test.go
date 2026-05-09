package audit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// failOnceRecorder fails its first N Record calls then succeeds. Used to
// exercise the drainer's retry path: the segment file must remain in place
// across failures and only get deleted after a successful pass.
type failOnceRecorder struct {
	mu       sync.Mutex
	fails    int
	captured []Event
}

func (f *failOnceRecorder) Record(_ context.Context, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return errors.New("transient")
	}
	f.captured = append(f.captured, ev)
	return nil
}

// blockingRecorder blocks on Record until its unblock channel is closed. Used
// to simulate a stuck inner recorder so the drainer cannot make progress.
// Callers must close the unblock channel (via unblockAll) before closing the
// WALRecorder so the drain-on-close path does not hang.
type blockingRecorder struct {
	mu       sync.Mutex
	once     sync.Once
	captured []Event
	unblock  chan struct{}
}

func newBlockingRecorder() *blockingRecorder {
	return &blockingRecorder{unblock: make(chan struct{})}
}

func (b *blockingRecorder) Record(ctx context.Context, ev Event) error {
	select {
	case <-b.unblock:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.captured = append(b.captured, ev)
	return nil
}

// unblockAll unblocks all pending and future Record calls. Idempotent.
func (b *blockingRecorder) unblockAll() {
	b.once.Do(func() { close(b.unblock) })
}

func (b *blockingRecorder) capturedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.captured)
}

// makeTestEvent builds a minimal read-class Event for use in tests.
func makeTestEvent() Event {
	return Event{
		Verb:   string(VerbNodeRead),
		Actor:  Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity: Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: EventContext{
			OrgID:  uuid.Must(uuid.NewV7()),
			Source: SourceMCP,
		},
	}
}

// writeEmptySegment writes a zero-byte .wal file with the given timestamp into
// dir. The file represents a segment that was closed but not yet drained.
// Empty segments are removed by the drainer on first scan (ReadFull returns
// EOF immediately) without ever calling the inner Recorder.
func writeEmptySegment(t *testing.T, dir string, ts time.Time) string {
	t.Helper()
	name := ts.UTC().Format("20060102T150405") + ".wal"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("writeEmptySegment: %v", err)
	}
	return path
}

// writeSegmentWithEvent writes a .wal file at the given timestamp containing
// exactly one length-prefixed JSON event. The event is real enough that the
// drainer's bufio reader will hand it to the inner Recorder. Use this when
// the test depends on a blocking inner Recorder to stall the drainer.
func writeSegmentWithEvent(t *testing.T, dir string, ts time.Time) string {
	t.Helper()
	name := ts.UTC().Format("20060102T150405") + ".wal"
	path := filepath.Join(dir, name)
	payload, err := json.Marshal(makeTestEvent())
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(payload)))
	body := append(lp[:], payload...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("writeSegmentWithEvent: %v", err)
	}
	return path
}

// countSegments returns the number of .wal files in dir.
func countSegments(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			count++
		}
	}
	return count
}

func TestWALAppendFsyncsAndDrains(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = w.Close() }()

	requestID := "req-wal"
	traceID := "trace-wal"
	for i := 0; i < 25; i++ {
		err := w.Record(t.Context(), Event{
			Verb:   string(VerbNodeRead),
			Actor:  Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
			Entity: Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
			Context: EventContext{
				OrgID:     uuid.Must(uuid.NewV7()),
				Source:    SourceMCP,
				RequestID: requestID,
				TraceID:   traceID,
			},
		})
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	// Close forces a final drain even when the active segment has not
	// rotated, which is the documented contract: every successfully
	// appended event reaches the inner Recorder by the time Close returns.
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	inner.mu.Lock()
	got := len(inner.captured)
	if got != 25 {
		inner.mu.Unlock()
		t.Fatalf("inner saw %d events, want 25", got)
	}
	first := inner.captured[0]
	inner.mu.Unlock()
	if first.Context.RequestID != requestID || first.Context.TraceID != traceID {
		t.Fatalf("correlation lost during replay: %#v", first.Context)
	}
}

func TestWALStateChangeBypassesQueue(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	err = w.Record(t.Context(), Event{
		Verb:    string(VerbNodeCreate),
		Actor:   Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: EventContext{OrgID: uuid.Must(uuid.NewV7())},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.captured) != 1 || inner.captured[0].Verb != string(VerbNodeCreate) {
		t.Fatalf("state-change verb did not bypass WAL: %#v", inner.captured)
	}
	// No segment file should have been written, since the verb skipped the WAL.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".wal" {
			t.Errorf("unexpected segment file %s", e.Name())
		}
	}
}

// TestIdleSegmentRotates verifies that the drainer force-rotates the active
// segment after it has been idle for longer than idleRotateAfter, drains it,
// and that the inner Recorder receives the event.
func TestIdleSegmentRotates(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:             dir,
		DrainInterval:   10 * time.Millisecond,
		IdleRotateAfter: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Append one event so a segment is created.
	if err := w.Record(t.Context(), makeTestEvent()); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Sleep long enough for idleRotateAfter to expire and for drainOnce to
	// force-rotate and drain the segment.
	time.Sleep(200 * time.Millisecond)

	// The active segment should have been rotated and drained.
	w.mu.Lock()
	currentPath := ""
	if w.current != nil {
		currentPath = w.current.path
	}
	w.mu.Unlock()

	// After draining, the segment file for the original write should be gone.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") && e.Name() != filepath.Base(currentPath) {
			t.Errorf("unexpected stale segment after idle rotation: %s", e.Name())
		}
	}

	// The event must have reached the inner Recorder.
	inner.mu.Lock()
	count := len(inner.captured)
	inner.mu.Unlock()
	if count < 1 {
		t.Errorf("inner Recorder received %d events, want >= 1", count)
	}

	// unflushedSegments should be back to 0 (no non-active segments remain).
	if got := w.unflushedSegments.Load(); got != 0 {
		t.Errorf("unflushedSegments = %d after drain, want 0", got)
	}

	// idleRotationsTotal must have been incremented at least once.
	if got := w.idleRotationsTotal.Load(); got < 1 {
		t.Errorf("idleRotationsTotal = %d, want >= 1", got)
	}
}

// TestBacklogDoesNotDropEvents injects more fake closed segments than the
// MaxBacklogSegments threshold and verifies that Record still succeeds. A
// behind drainer is queueing, not failure. Each segment carries one real
// event so a blocking inner Recorder actually stalls the drainer.
func TestBacklogDoesNotDropEvents(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 65; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i+1) * time.Minute)
		writeSegmentWithEvent(t, dir, ts)
	}
	preexisting := countSegments(t, dir)

	inner := newBlockingRecorder()
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:                dir,
		DrainInterval:      10 * time.Millisecond,
		MaxBacklogSegments: 64,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for drainOnce to scan and populate the backlog atomics so the
	// BacklogSignal observability path is exercised.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if w.unflushedSegments.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.unflushedSegments.Load() == 0 {
		inner.unblockAll()
		_ = w.Close()
		t.Fatal("drainOnce did not update unflushedSegments within 500ms")
	}

	// BacklogSignal should now report true: pure observability.
	if !w.BacklogSignal(64, 10*time.Minute) {
		inner.unblockAll()
		_ = w.Close()
		t.Fatal("BacklogSignal did not flip with 65 backlog segments")
	}

	// Record must succeed despite the backlog.
	err = w.Record(t.Context(), makeTestEvent())
	if err != nil {
		inner.unblockAll()
		_ = w.Close()
		t.Fatalf("Record returned %v during backlog, want nil", err)
	}

	// A new segment for the just-written event must exist on disk.
	if got := countSegments(t, dir); got <= preexisting {
		inner.unblockAll()
		_ = w.Close()
		t.Errorf("segment count after Record = %d, want > %d (a new active segment)",
			got, preexisting)
	}

	// Unblock and clean up.
	inner.unblockAll()
	_ = w.Close()
}

// TestOldSegmentDoesNotDropEvents injects one segment older than
// MaxBacklogAge and verifies that Record still succeeds. The segment carries
// one real event so the blocking inner Recorder actually stalls the drainer.
func TestOldSegmentDoesNotDropEvents(t *testing.T) {
	dir := t.TempDir()
	writeSegmentWithEvent(t, dir, time.Now().UTC().Add(-20*time.Minute))

	inner := newBlockingRecorder()
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: 10 * time.Millisecond,
		MaxBacklogAge: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for drainOnce to scan.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if w.oldestUnflushedUnixNano.Load() != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.oldestUnflushedUnixNano.Load() == 0 {
		inner.unblockAll()
		_ = w.Close()
		t.Fatal("drainOnce did not update oldestUnflushedUnixNano within 500ms")
	}

	if !w.BacklogSignal(64, 10*time.Minute) {
		inner.unblockAll()
		_ = w.Close()
		t.Fatal("BacklogSignal did not flip on age threshold")
	}

	err = w.Record(t.Context(), makeTestEvent())
	if err != nil {
		inner.unblockAll()
		_ = w.Close()
		t.Fatalf("Record returned %v, want nil", err)
	}

	// At least one segment must exist (the one that just received the event,
	// plus the pre-existing old one if not yet drained).
	if got := countSegments(t, dir); got < 1 {
		inner.unblockAll()
		_ = w.Close()
		t.Errorf("segment count = %d, want >= 1", got)
	}

	inner.unblockAll()
	_ = w.Close()
}

// TestBacklogClearsAfterDrainerRecovery sets up a backlog with a blocked
// drainer, verifies Record succeeds during the backup, then unblocks the
// drainer and verifies the backlog signal returns to zero and a follow-up
// Record still succeeds. Each injected segment carries one event so the
// blocking inner Recorder actually stalls the drainer.
func TestBacklogClearsAfterDrainerRecovery(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 65; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i+1) * time.Minute)
		writeSegmentWithEvent(t, dir, ts)
	}
	inner := newBlockingRecorder()
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:                dir,
		DrainInterval:      20 * time.Millisecond,
		MaxBacklogSegments: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// Wait for atomics to reflect the backlog.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if w.unflushedSegments.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.unflushedSegments.Load() == 0 {
		t.Fatal("drainOnce did not update unflushedSegments within 500ms")
	}

	// Record must succeed even while the drainer is blocked.
	if err := w.Record(t.Context(), makeTestEvent()); err != nil {
		t.Fatalf("Record during backup returned %v, want nil", err)
	}

	// Unblock the inner recorder so the drainer can flush.
	inner.unblockAll()

	// Wait for the backlog to clear.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.unflushedSegments.Load() == 0 && w.oldestUnflushedUnixNano.Load() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w.unflushedSegments.Load() != 0 {
		t.Fatalf("drainer did not clear backlog within 2s; unflushedSegments=%d",
			w.unflushedSegments.Load())
	}
	if w.BacklogSignal(64, 10*time.Minute) {
		t.Errorf("BacklogSignal still true after recovery")
	}

	// Subsequent Record must still succeed.
	if err := w.Record(t.Context(), makeTestEvent()); err != nil {
		t.Errorf("Record after recovery returned %v, want nil", err)
	}
}

// TestStateChangeBypassesWAL verifies that state-change verbs reach the inner
// Recorder synchronously even when the WAL backlog is above threshold. The
// gate is gone but the bypass remains: state-change verbs never touch the WAL.
func TestStateChangeBypassesWAL(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 65; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i+1) * time.Minute)
		writeEmptySegment(t, dir, ts)
	}
	inner := newBlockingRecorder()
	// Pre-unblock so state-change Records are not gated by the inner recorder.
	inner.unblockAll()

	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:                dir,
		DrainInterval:      10 * time.Millisecond,
		MaxBacklogSegments: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// Wait for the backlog atomics to be populated.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if w.unflushedSegments.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	stateEv := Event{
		Verb:    string(VerbNodeCreate),
		Actor:   Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: EventContext{OrgID: uuid.Must(uuid.NewV7())},
	}
	if err := w.Record(t.Context(), stateEv); err != nil {
		t.Errorf("state-change Record returned %v, want nil", err)
	}

	if got := inner.capturedCount(); got < 1 {
		t.Errorf("inner Recorder saw %d events, want >= 1 after state-change bypass", got)
	}
}

// TestRotationDuringHighThroughput appends 10k events at concurrency 32 with
// a very short idleRotateAfter to interleave forced rotations with hot writes.
// Asserts no panics, no data loss, and that the drainer keeps up within a
// reasonable window.
func TestRotationDuringHighThroughput(t *testing.T) {
	dir := t.TempDir()
	var received atomic.Int64
	countingRecorder := &struct{ Recorder }{
		Recorder: recorderFunc(func(_ context.Context, _ Event) error {
			received.Add(1)
			return nil
		}),
	}
	w, err := NewWALRecorder(t.Context(), countingRecorder, WALConfig{
		Dir:             dir,
		DrainInterval:   5 * time.Millisecond,
		IdleRotateAfter: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const concurrency = 32
	const perGoroutine = 312
	const total = concurrency * perGoroutine // 9984; concurrency must divide total
	var wg sync.WaitGroup
	var errCount atomic.Int64
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if err := w.Record(t.Context(), makeTestEvent()); err != nil {
					errCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if errCount.Load() > 0 {
		t.Errorf("got %d Record errors during high-throughput test", errCount.Load())
	}

	// Every event that was successfully appended must have been drained.
	appended := int64(total) - errCount.Load()
	if got := received.Load(); got != appended {
		t.Errorf("inner Recorder received %d events, want %d (appended)", got, appended)
	}
}

// TestPreflightDrainOnRestart pre-populates the WAL dir with a closed segment
// containing 4 events, creates a WALRecorder, runs one drain cycle, and
// verifies all 4 events reach the inner Recorder without any new appends.
func TestPreflightDrainOnRestart(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}

	// Build a segment file containing 4 events by writing through a
	// temporary WALRecorder and then closing it without draining to the inner.
	captureDir := t.TempDir()
	captureInner := &failOnceRecorder{fails: 9999} // will always fail, so segment stays
	cw, err := NewWALRecorder(t.Context(), captureInner, WALConfig{
		Dir:           captureDir,
		DrainInterval: time.Hour, // drainer never fires during this setup
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := cw.Record(t.Context(), makeTestEvent()); err != nil {
			t.Fatalf("capture record %d: %v", i, err)
		}
	}
	// Stop the drainer without draining, then flush the active segment to disk.
	close(cw.stop)
	<-cw.stopped
	cw.mu.Lock()
	segPath := ""
	if cw.current != nil {
		segPath = cw.current.path
		_ = cw.current.flushAndClose()
		cw.current = nil
	}
	cw.mu.Unlock()
	if segPath == "" {
		t.Fatal("no active segment was created")
	}

	// Copy the segment file into the real WAL dir.
	segData, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatal(err)
	}
	destName := filepath.Base(segPath)
	if err := os.WriteFile(filepath.Join(dir, destName), segData, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create the real WALRecorder (simulating a restart).
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the drainer to pick up the pre-existing segment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inner.mu.Lock()
		count := len(inner.captured)
		inner.mu.Unlock()
		if count >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	inner.mu.Lock()
	got := len(inner.captured)
	inner.mu.Unlock()
	if got != 4 {
		t.Errorf("inner Recorder received %d events after preflight drain, want 4", got)
	}

	// The WAL dir should be empty (segment drained and removed).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			t.Errorf("segment still present after drain: %s", e.Name())
		}
	}
}

// enospcRecorder is an inner recorder used only as a stand-in; the actual
// ENOSPC injection happens at the segment-write layer via writeFn.
//
// failingFile is an os.File-style writer that returns syscall.ENOSPC on every
// Write call. We exercise the ENOSPC path by replacing the active segment's
// bufio.Writer with one fronted by failingFile, then issuing a Record.
type failingWriter struct {
	err error
}

func (f *failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

// TestRecordReturnsErrorOnENOSPC simulates a disk-write that fails with
// syscall.ENOSPC and verifies that Record propagates the error to the caller
// with no silent retry, and that writeErrorsTotal increments.
func TestRecordReturnsErrorOnENOSPC(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: time.Hour, // drainer must not interfere
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// Force the active segment to exist by writing one good event.
	if err := w.Record(t.Context(), makeTestEvent()); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// Replace the active segment's underlying writer with one that fails
	// every Write with syscall.ENOSPC. The bufio.Writer above it will
	// surface the error on Flush.
	w.mu.Lock()
	if w.current == nil {
		w.mu.Unlock()
		t.Fatal("no active segment after seed write")
	}
	w.current.w.Reset(&failingWriter{err: syscall.ENOSPC})
	w.mu.Unlock()

	before := w.writeErrorsTotal.Load()

	err = w.Record(t.Context(), makeTestEvent())
	if err == nil {
		t.Fatal("Record returned nil, want syscall.ENOSPC")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("Record returned %v, want errors.Is(err, syscall.ENOSPC)", err)
	}

	if got := w.writeErrorsTotal.Load(); got <= before {
		t.Errorf("writeErrorsTotal = %d, want > %d", got, before)
	}
}

// TestDiskPressureWarningEmitted injects a Statfs result that reports very
// low free space and verifies that drainOnce emits the audit.wal.disk_pressure
// Warn slog. Record must continue to succeed; the pressure signal is purely
// observational.
func TestDiskPressureWarningEmitted(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}

	// Capture slog output. Use a JSON handler at Warn level so the
	// disk_pressure line is included. The buffer is wrapped in a mutex
	// because the drainer goroutine writes to it while the test reads it.
	logBuf := &lockedBuffer{}
	handler := slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(prevDefault)

	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// Inject a fake Statfs reporting 1 free block of 4 KiB on a 1 GiB volume.
	// The setter takes statfsMu so the drainer goroutine sees the new probe
	// without racing.
	w.statfsMu.Lock()
	w.statfs = func(_ string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 262144 // 1 GiB total
		buf.Bavail = 1      // ~4 KiB free, well under the 100 MiB floor
		return nil
	}
	w.statfsMu.Unlock()

	// Wait for at least one drainOnce cycle to run sampleDiskFree.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.snapshot(), "audit.wal.disk_pressure") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(logBuf.snapshot(), "audit.wal.disk_pressure") {
		t.Errorf("expected audit.wal.disk_pressure warn in logs; got:\n%s", logBuf.snapshot())
	}

	// Disk-free gauge should reflect the injected free byte count.
	if got := w.diskFreeBytes.Load(); got == 0 {
		t.Errorf("diskFreeBytes = 0, want > 0 after sampleDiskFree")
	}

	// Record must still succeed: pressure signal does not gate writes.
	if err := w.Record(t.Context(), makeTestEvent()); err != nil {
		t.Errorf("Record under disk pressure returned %v, want nil", err)
	}
}

// recorderFunc adapts a plain function to the Recorder interface.
type recorderFunc func(ctx context.Context, ev Event) error

func (f recorderFunc) Record(ctx context.Context, ev Event) error { return f(ctx, ev) }

// lockedBuffer is a goroutine-safe wrapper around bytes.Buffer used by the
// disk-pressure test, where the drainer goroutine writes via slog and the
// test goroutine reads to assert on the captured output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
