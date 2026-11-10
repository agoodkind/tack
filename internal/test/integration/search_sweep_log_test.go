package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const (
	// searchLogPrefix defines the "search." message prefix. The sweep test
	// rejects every ERROR record with a message that starts with this prefix.
	// The throughput check in TestSearchActualOSProcessThroughput applies
	// this rule.
	searchLogPrefix = "search."
	// sweepSkippedMessage is the debug record of a session that no longer
	// exists.
	sweepSkippedMessage = "search.session.sweep_skipped"
	// sessionDeletedMessage is the record of a removed session header.
	sessionDeletedMessage = "search.session.deleted"
)

// sweepLogRecord is one JSON log record of the search service.
type sweepLogRecord struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Err     string `json:"err"`
}

// lockedLog is a log destination that concurrent searches share.
type lockedLog struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (l *lockedLog) Write(record []byte) (int, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	written, err := l.buffer.Write(record)
	if err != nil {
		return written, fmt.Errorf("write a log record: %w", err)
	}
	return written, nil
}

func (l *lockedLog) contents() []byte {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return bytes.Clone(l.buffer.Bytes())
}

// captureSweepLog captures default slog output in a new lockedLog at Debug
// level. slog.SetDefault replaces the log package writer and clears its flags
// when it uses a custom handler. slog.SetDefault leaves that writer in place
// when it uses the built-in handler. Later tests lose their log output when
// the writer remains replaced. captureSweepLog returns the lockedLog and a
// restore function. The restore function restores the earlier default logger,
// then restores the log package writer and flags.
func captureSweepLog(t *testing.T) (*lockedLog, func()) {
	t.Helper()
	logged := &lockedLog{mutex: sync.Mutex{}, buffer: bytes.Buffer{}}
	previous := slog.Default()
	writer := log.Writer()
	flags := log.Flags()
	restore := func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	}
	t.Cleanup(restore)
	slog.SetDefault(slog.New(slog.NewJSONHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return logged, restore
}

// requireSweepSkip requires the captured JSON log records to contain exactly
// one Debug record with the message search.session.sweep_skipped. It also
// requires exactly deleted records with the message search.session.deleted. It
// rejects every ERROR record with a message that starts with the "search."
// prefix.
func requireSweepSkip(t *testing.T, logged []byte, deleted int) {
	t.Helper()
	skipped, found := 0, 0
	decoder := json.NewDecoder(bytes.NewReader(logged))
	for {
		var record sweepLogRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode a search log record: %v", err)
		}
		if record.Level == slog.LevelError.String() && strings.HasPrefix(record.Message, searchLogPrefix) {
			t.Errorf("the searches logged %s %s with error %q", record.Level, record.Message, record.Err)
		}
		if record.Level == slog.LevelDebug.String() && record.Message == sweepSkippedMessage {
			skipped++
		}
		if record.Message == sessionDeletedMessage {
			found++
		}
	}
	if skipped != 1 {
		t.Errorf("the sweeps logged %d %s records, want 1", skipped, sweepSkippedMessage)
	}
	if found != deleted {
		t.Errorf("the sweeps logged %d %s records, want %d", found, sessionDeletedMessage, deleted)
	}
}
