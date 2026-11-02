//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// sessionWriteCapture stores the session ID and audit operation from the
// context of the first log record with message.
type sessionWriteCapture struct {
	message   string
	mutex     sync.Mutex
	captured  bool
	sessionID uuid.UUID
	operation audit.Event
	err       error
}

func installSessionWriteCapture(message string) *sessionWriteCapture {
	capture := &sessionWriteCapture{
		message: message, mutex: sync.Mutex{}, captured: false, sessionID: uuid.Nil,
		operation: audit.Event{}, err: errors.New("the log record did not appear"),
	}
	slog.SetDefault(slog.New(sessionWriteHandler{Handler: slog.Default().Handler(), capture: capture}))
	return capture
}

func (c *sessionWriteCapture) observe(ctx context.Context, record slog.Record) {
	if record.Message != c.message {
		return
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.captured {
		return
	}
	c.captured = true
	c.operation, c.err = audit.RequireMutationContext(ctx)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "session_id" {
			parsed, parseErr := uuid.Parse(attr.Value.String())
			c.sessionID, c.err = parsed, errors.Join(c.err, parseErr)
		}
		return true
	})
}

func (c *sessionWriteCapture) result() (uuid.UUID, audit.Event, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.sessionID, c.operation, c.err
}

type sessionWriteHandler struct {
	slog.Handler
	capture *sessionWriteCapture
}

func (h sessionWriteHandler) Handle(ctx context.Context, record slog.Record) error {
	h.capture.observe(ctx, record)
	return h.Handler.Handle(ctx, record)
}
