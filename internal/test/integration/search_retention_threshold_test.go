package integration

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// retentionUnreachedAge is a retirement age limit no test retirement exceeds.
	retentionUnreachedAge = 1000 * time.Hour
	// retentionStartedMessage is the log record SearchRetention.Check writes
	// with the reason of each replacement it begins (search_retention.go:90-92).
	retentionStartedMessage = "search.retention.replacement_started"
	// shortRetentionText projects to one page at the recovery page size.
	shortRetentionText = "short retention text"
)

// retentionUnreached returns limits above every measured value.
func retentionUnreached() config.SearchRetentionSettings {
	return config.SearchRetentionSettings{
		MaxRetiredPages: math.MaxInt64, MaxRetirementAge: retentionUnreachedAge,
		MaxIndexBytes: math.MaxInt64, CheckInterval: time.Minute,
	}
}

// retentionRecord is the part of a JSON log record the test reads.
type retentionRecord struct {
	Message string `json:"msg"`
	Reason  string `json:"reason"`
}

// TestSearchRetentionThresholdsStartFullReplacement indexes a multi-page node,
// shortens it to retire its old pages, and measures the serving index. Each
// subtest runs SearchRetention.Check on a new store and index with one limit
// that every such index exceeds and the other two above any measured value.
// It requires a full FoundationDB replacement of the serving index and a
// replacement reason that states the crossed limit. With every limit above
// its measured value, Check begins no replacement.
func TestSearchRetentionThresholdsStartFullReplacement(t *testing.T) {
	cases := []struct {
		name   string
		limits func(*config.SearchRetentionSettings)
		reason string
	}{
		{"retired pages", func(limits *config.SearchRetentionSettings) { limits.MaxRetiredPages = 0 }, "retired pages "},
		{"primary bytes", func(limits *config.SearchRetentionSettings) { limits.MaxIndexBytes = 1 }, "primary bytes "},
		{"retirement age", func(limits *config.SearchRetentionSettings) { limits.MaxRetirementAge = time.Nanosecond }, "oldest retirement age "},
		{"no limit crossed", func(*config.SearchRetentionSettings) {}, ""},
	}
	for _, retentionCase := range cases {
		t.Run(retentionCase.name, func(t *testing.T) {
			stores := newSearchStore(t)
			adapter, client, _, serving := newSearchIndex(t, stores)
			source := clock.Wall{}
			settings := searchWorkerSettings(recoveryPageBytes)
			fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
			runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
			writeSearchNode(t, stores, fixture, shortRetentionText, readerExcludedValue)
			runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
			if retired := searchNodePages(t, client, serving, fixture.NodeID, true); len(retired) < 2 {
				t.Fatalf("shortening the node retired %d pages, want at least 2", len(retired))
			}
			stats, err := adapter.IndexRetention(t.Context(), serving)
			if err != nil || stats.RetiredPages < 2 || stats.PrimaryBytes < 2 {
				t.Fatalf("index retention = %+v err %v, want at least 2 retired pages and 2 primary bytes", stats, err)
			}
			rebuilds := stores.SearchRebuilds(source)
			if _, retired, err := rebuilds.RetiredSince(t.Context(), serving); err != nil || !retired {
				t.Fatalf("retired since: retired %t err %v, want a recorded retirement", retired, err)
			}
			t.Logf("%s: serving index %s has %d retired pages and %d primary bytes", retentionCase.name, serving, stats.RetiredPages, stats.PrimaryBytes)

			limits := retentionUnreached()
			retentionCase.limits(&limits)
			topology := config.SearchTopology{Primaries: 1, RoutingShards: 8, Replicas: 0}
			retention := service.NewSearchRetention(rebuilds, adapter, stores, source, limits, topology)
			var logged bytes.Buffer
			checkContext := telemetry.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logged, nil)))
			if err := retention.Check(checkContext); err != nil {
				t.Fatalf("retention check: %v", err)
			}
			reasons := retentionReasons(t, logged.Bytes())
			current, found, err := rebuilds.CurrentRebuild(t.Context())
			if err != nil {
				t.Fatalf("read index replacement: %v", err)
			}
			if found {
				t.Cleanup(func() { deleteNativeIndex(t, client, current.TargetIndex) })
			}
			if retentionCase.reason == "" {
				if found || len(reasons) != 0 {
					t.Fatalf("retention check began replacement %+v with reasons %q and no limit crossed", current, reasons)
				}
				return
			}
			if !found || current.Mode != searchdomain.ReplacementFull || current.SourceIndex != serving {
				t.Fatalf("index replacement = %+v found %t, want a full replacement of %s", current, found, serving)
			}
			if len(reasons) != 1 || !strings.HasPrefix(reasons[0], retentionCase.reason) {
				t.Fatalf("replacement reasons = %q, want one reason that starts with %q", reasons, retentionCase.reason)
			}
			t.Logf("%s: replacement reason %q", retentionCase.name, reasons[0])
		})
	}
}

// retentionReasons returns the reason of every replacement-started record in
// the JSON log output of one check.
func retentionReasons(t *testing.T, output []byte) []string {
	t.Helper()
	var reasons []string
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record retentionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode retention log record %q: %v", line, err)
		}
		if record.Message == retentionStartedMessage {
			reasons = append(reasons, record.Reason)
		}
	}
	return reasons
}
