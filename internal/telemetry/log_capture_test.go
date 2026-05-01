package telemetry

import (
	"context"
	"log/slog"
	"slices"
	"sync"
)

type capturedLogRecord struct {
	message string
	attrs   map[string]slog.Value
}

type captureHandler struct {
	mu      *sync.Mutex
	attrs   []slog.Attr
	records *[]capturedLogRecord
}

func newCaptureHandler() *captureHandler {
	records := []capturedLogRecord{}
	return &captureHandler{
		mu:      &sync.Mutex{},
		records: &records,
	}
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]slog.Value)
	for _, attr := range h.attrs {
		attrs[attr.Key] = attr.Value
	}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, capturedLogRecord{
		message: record.Message,
		attrs:   attrs,
	})
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{
		mu:      h.mu,
		attrs:   append(slices.Clone(h.attrs), attrs...),
		records: h.records,
	}
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureHandler) find(message string) (capturedLogRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range *h.records {
		if record.message == message {
			return record, true
		}
	}
	return capturedLogRecord{}, false
}
