//go:build runtimeaudit

package integration_test

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// cleanupLossSweepMessage identifies the session sweep failure log.
	cleanupLossSweepMessage = "search.session.sweep_failed"
	// cleanupLossAuditMessage identifies the cleanup audit save failure log.
	cleanupLossAuditMessage = "search.session.audit_failed"
	// cleanupLossStepMessage identifies the snapshot step failure log.
	cleanupLossStepMessage = "search.snapshot.step_failed"
	// cleanupLossFailureMessage identifies the search query failure log.
	cleanupLossFailureMessage = "search.query.failed"
	// cleanupLossPauseLimit limits the duration of each Docker pause or resume call.
	cleanupLossPauseLimit = 30 * time.Second
)

// cleanupLossGate pauses and resumes the test container at selected events.
// It keeps its mutex locked during each Docker call. install replaces the
// default logger with a text handler on standard error. Cleanup restores the
// previous default logger and the log package output and flags, and resumes
// the container if it is paused.
type cleanupLossGate struct {
	t          *testing.T
	container  string
	pauseWhen  func(slog.Record) bool
	resumeWhen func(slog.Record) bool
	mutex      sync.Mutex
	paused     bool
	failure    string
	pausedSeen chan struct{}
	resumeSeen chan struct{}
}

func newCleanupLossGate(t *testing.T, container string, pauseWhen, resumeWhen func(slog.Record) bool) *cleanupLossGate {
	t.Helper()
	return &cleanupLossGate{
		t: t, container: container, pauseWhen: pauseWhen, resumeWhen: resumeWhen, mutex: sync.Mutex{}, paused: false,
		pausedSeen: make(chan struct{}), resumeSeen: make(chan struct{}),
	}
}

func cleanupLossSessionLog(message, session string) func(slog.Record) bool {
	return func(record slog.Record) bool {
		matched := false
		record.Attrs(func(attr slog.Attr) bool {
			matched = matched || (attr.Key == "session_id" && attr.Value.String() == session)
			return true
		})
		return record.Message == message && matched
	}
}

func cleanupLossStepLog(record slog.Record) bool {
	matched := false
	record.Attrs(func(attr slog.Attr) bool {
		matched = matched || (attr.Key == "err" && strings.Contains(attr.Value.String(), "record external step result"))
		return true
	})
	return record.Message == cleanupLossStepMessage && matched
}

func (g *cleanupLossGate) install() {
	g.t.Helper()
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(cleanupLossHandler{Handler: base, gate: g}))
	g.t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	g.t.Cleanup(g.resume)
}

func (g *cleanupLossGate) observe(record slog.Record) {
	switch {
	case record.Message == cleanupLossFailureMessage:
		g.keepFailure(record)
	case g.pauseWhen != nil && g.pauseWhen(record):
		g.pause()
	case g.resumeWhen(record):
		g.resume()
	}
}

func (g *cleanupLossGate) keepFailure(record slog.Record) {
	select {
	case <-g.pausedSeen:
	default:
		return
	}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "err" {
			g.mutex.Lock()
			g.failure = attr.Value.String()
			g.mutex.Unlock()
		}
		return true
	})
}

func (g *cleanupLossGate) failureText() string {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return g.failure
}

func (g *cleanupLossGate) pause() {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	select {
	case <-g.pausedSeen:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupLossPauseLimit)
	defer cancel()
	if err := changeContainerPause(ctx, g.container, true); err != nil {
		g.t.Errorf("pause container %s: %v", g.container, err)
		return
	}
	g.paused = true
	close(g.pausedSeen)
}

func (g *cleanupLossGate) resume() {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if !g.paused {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupLossPauseLimit)
	defer cancel()
	if err := changeContainerPause(ctx, g.container, false); err != nil {
		g.t.Errorf("resume container %s: %v", g.container, err)
		return
	}
	g.paused = false
	select {
	case <-g.resumeSeen:
	default:
		close(g.resumeSeen)
	}
}

type cleanupLossHandler struct {
	slog.Handler
	gate *cleanupLossGate
}

func (h cleanupLossHandler) Handle(ctx context.Context, record slog.Record) error {
	h.gate.observe(record)
	return h.Handler.Handle(ctx, record)
}

func (h cleanupLossHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return cleanupLossHandler{Handler: h.Handler.WithAttrs(attrs), gate: h.gate}
}

func (h cleanupLossHandler) WithGroup(name string) slog.Handler {
	return cleanupLossHandler{Handler: h.Handler.WithGroup(name), gate: h.gate}
}
