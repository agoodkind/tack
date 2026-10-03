package integration

import (
	"math"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// retentionUnreachedAge is a retirement age limit no test retirement exceeds.
const retentionUnreachedAge = 1000 * time.Hour

// retentionUnreached returns limits above every measured value.
func retentionUnreached() config.SearchRetentionSettings {
	return config.SearchRetentionSettings{
		MaxRetiredPages: math.MaxInt64, MaxRetirementAge: retentionUnreachedAge,
		MaxIndexBytes: math.MaxInt64, CheckInterval: time.Minute,
	}
}

// TestSearchRetentionThresholdsStartFullReplacement indexes a multi-page node,
// shortens it to retire its old pages, and measures the serving index. Each
// subtest runs SearchRetention.Check on a new store and index with one limit
// below its measured value and the other two above theirs, and requires a
// full FoundationDB replacement of the serving index. With every limit above
// its measured value, Check begins no replacement.
func TestSearchRetentionThresholdsStartFullReplacement(t *testing.T) {
	cases := []struct {
		name    string
		limits  func(searchdomain.RetentionStats) config.SearchRetentionSettings
		replace bool
	}{
		{"retired pages", func(stats searchdomain.RetentionStats) config.SearchRetentionSettings {
			limits := retentionUnreached()
			limits.MaxRetiredPages = stats.RetiredPages - 1
			return limits
		}, true},
		{"primary bytes", func(stats searchdomain.RetentionStats) config.SearchRetentionSettings {
			limits := retentionUnreached()
			limits.MaxIndexBytes = stats.PrimaryBytes - 1
			return limits
		}, true},
		{"retirement age", func(searchdomain.RetentionStats) config.SearchRetentionSettings {
			limits := retentionUnreached()
			limits.MaxRetirementAge = time.Nanosecond
			return limits
		}, true},
		{"no limit crossed", func(searchdomain.RetentionStats) config.SearchRetentionSettings {
			return retentionUnreached()
		}, false},
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
			if retired := searchNodePages(t, client, serving, fixture.NodeID, true); len(retired) == 0 {
				t.Fatal("shortening the node retired no page")
			}
			stats, err := adapter.IndexRetention(t.Context(), serving)
			if err != nil || stats.RetiredPages < 1 || stats.PrimaryBytes < 1 {
				t.Fatalf("index retention = %+v err %v, want retired pages and primary bytes", stats, err)
			}
			rebuilds := stores.SearchRebuilds(source)
			if _, retired, err := rebuilds.RetiredSince(t.Context(), serving); err != nil || !retired {
				t.Fatalf("retired since: retired %t err %v, want a recorded retirement", retired, err)
			}
			t.Logf("%s: serving index %s has %d retired pages and %d primary bytes", retentionCase.name, serving, stats.RetiredPages, stats.PrimaryBytes)

			topology := config.SearchTopology{Primaries: 1, RoutingShards: 8, Replicas: 0}
			retention := service.NewSearchRetention(rebuilds, adapter, stores, source, retentionCase.limits(stats), topology)
			if err := retention.Check(t.Context()); err != nil {
				t.Fatalf("retention check: %v", err)
			}
			current, found, err := rebuilds.CurrentRebuild(t.Context())
			if err != nil {
				t.Fatalf("read index replacement: %v", err)
			}
			if found {
				t.Cleanup(func() { deleteNativeIndex(t, client, current.TargetIndex) })
			}
			if !retentionCase.replace {
				if found {
					t.Fatalf("retention check began replacement %+v with no limit crossed", current)
				}
				return
			}
			if !found || current.Mode != searchdomain.ReplacementFull || current.SourceIndex != serving {
				t.Fatalf("index replacement = %+v found %t, want a full replacement of %s", current, found, serving)
			}
		})
	}
}

// shortRetentionText projects to one page at the recovery page size.
const shortRetentionText = "short retention text"
