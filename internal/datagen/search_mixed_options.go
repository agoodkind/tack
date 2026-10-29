package datagen

import (
	"errors"
	"fmt"
)

// unmeasuredSearchMixedSummary returns the planned counts of a run that did not start the
// measured window.
func unmeasuredSearchMixedSummary(options SearchMixedOptions, dryRun bool, plannedSearches, plannedWrites int) SearchMixedSummary {
	searches := (&searchLoadStats{}).summary(dryRun, plannedSearches, 0, 0, 0, 0)
	summary := (&searchMixedStats{}).summary(searches, plannedWrites, 0)
	summary.Endpoints, summary.Burst.Planned = len(options.Endpoints), options.BurstWrites
	summary.Changes.MetadataPlanned, summary.Changes.MovesPlanned = options.MetadataWrites, options.Moves
	return summary
}

func validateSearchMixedOptions(options SearchMixedOptions) error {
	if options.Duration <= 0 {
		return errors.New("qa datagen search-mixed: duration must be positive")
	}
	if options.Concurrency <= 0 {
		return errors.New("qa datagen search-mixed: concurrency must be positive")
	}
	if options.Rate < 0 || options.WriteRate < 0 || options.BurstWrites < 0 {
		return errors.New("qa datagen search-mixed: rate, write rate, and burst writes must not be negative")
	}
	if options.CatchUp <= 0 {
		return errors.New("qa datagen search-mixed: catch-up timeout must be positive")
	}
	if options.Commit && (options.Rate == 0 || options.WriteRate == 0 || options.BurstWrites == 0) {
		return errors.New("qa datagen search-mixed: rate, write rate, and burst writes must be positive with --commit")
	}
	if len(options.Endpoints) > 0 && options.Client == nil {
		return errors.New("qa datagen search-mixed: endpoints require an HTTP client")
	}
	return nil
}

// validateSearchMixedChanges rejects negative MetadataWrites and Moves values. It also rejects
// MetadataWrites+Moves above plannedWrites. Each change replaces one scheduled write.
func validateSearchMixedChanges(options SearchMixedOptions, plannedWrites int) error {
	if options.MetadataWrites < 0 || options.Moves < 0 {
		return errors.New("qa datagen search-mixed: metadata writes and moves must not be negative")
	}
	if options.MetadataWrites+options.Moves > plannedWrites {
		return fmt.Errorf("qa datagen search-mixed: %d metadata writes and %d moves exceed the %d planned writes",
			options.MetadataWrites, options.Moves, plannedWrites)
	}
	return nil
}
