package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// Verify returns an error for each correctness check that the run failed. It returns nil when
// every check passed.
func (s SearchMixedSummary) Verify(ctx context.Context, stopReason string) error {
	failures := make([]error, 0)
	add := func(failed bool, format string, value int) {
		if failed {
			failures = append(failures, fmt.Errorf(format, value))
		}
	}
	add(s.DeletedReturned > 0, "search returned %d deleted issues after indexing processed the deletions", s.DeletedReturned)
	add(s.Pending.DeletedNodesReturned > 0, "search returned %d deleted issues during the pending deletion check", s.Pending.DeletedNodesReturned)
	add(s.Sessions.DeletedNodesReturned > 0, "search sessions returned %d deleted issues", s.Sessions.DeletedNodesReturned)
	add(s.Sessions.CrossTenantResults > 0, "search sessions returned %d issues of another organization", s.Sessions.CrossTenantResults)
	add(s.Sessions.ContinuationErrors > 0, "search sessions had %d continuation errors", s.Sessions.ContinuationErrors)
	add(s.Sessions.MissingCorpusIssues > 0, "search sessions did not return %d expected read issues", s.Sessions.MissingCorpusIssues)
	add(s.Sessions.ReplayMismatches > 0, "search sessions had %d replay mismatches", s.Sessions.ReplayMismatches)
	add(s.Revocation.ResultsAfterCacheLifetime > 0, "search returned %d issues to a removed member after the membership cache lifetime", s.Revocation.ResultsAfterCacheLifetime)
	add(s.Revocation.Checks > 0 && !s.Revocation.Refused, "search did not refuse a removed member within %d checks", s.Revocation.Checks)
	if s.Rebuild.FinalState == string(searchdomain.RebuildFailed) {
		failures = append(failures, errors.New("the index rebuild failed"))
	}
	// A dry run skips this block. Verify fails every other stopped run with one error that states
	// the stop reason, whether the run stopped during setup or during the measured window.
	// After a measured run that finished, Verify fails for incomplete catch-up, markers the probe
	// did not find, and deletions the probe did not verify.
	if s.Stopped && !s.Search.DryRun {
		failures = append(failures, fmt.Errorf("run stopped with reason %s; the measured window did not run or did not finish", stopReason))
	} else if s.Measured {
		classes := "live, access, or cleanup"
		if s.Changes.MetadataPlanned > 0 {
			classes = "live, access, cleanup, or rescan"
		}
		if !s.CatchUp.Completed {
			failures = append(failures, fmt.Errorf("%s search work with enqueue times before the end of writes remained pending after %s of catch-up", classes, s.CatchUp.Elapsed))
		}
		add(s.MarkersNotFound > 0, "search did not return %d written issues for their marker words", s.MarkersNotFound)
		add(s.DeletesUnverified > 0, "the command could not verify the removal of %d deleted issues from search results", s.DeletesUnverified)
		failures = append(failures, s.Changes.failures()...)
	}
	if len(failures) == 0 {
		return nil
	}
	joined := errors.Join(failures...)
	slog.ErrorContext(ctx, "qa.datagen.search_mixed_checks_failed", slog.String("err", joined.Error()))
	return joined
}

// SearchMixedChanges reports planned and committed metadata changes and moves, metadata values
// that search did not return, and moves that the command could not verify.
type SearchMixedChanges struct {
	MetadataPlanned  int
	MetadataChanges  int
	MetadataNotFound int
	MovesPlanned     int
	Moves            int
	MovesUnverified  int
	// MetadataAbsentBefore counts searches that did not return the issue for its excluded property
	// value before the metadata change.
	MetadataAbsentBefore int
	// MetadataPresentBefore counts searches that returned the issue for its excluded property value
	// before the metadata change.
	MetadataPresentBefore int
	// MetadataUncheckedBefore counts failed searches for the issue's excluded property value
	// before the metadata change.
	MetadataUncheckedBefore int
}

// failures returns errors when the run commits fewer metadata changes or moves than planned,
// when search does not return a changed metadata value, or when the command cannot verify a move.
func (c SearchMixedChanges) failures() []error {
	failures := make([]error, 0)
	if c.MetadataChanges < c.MetadataPlanned {
		failures = append(failures, fmt.Errorf("the run committed %d of %d planned metadata changes", c.MetadataChanges, c.MetadataPlanned))
	}
	if c.MetadataPresentBefore > 0 {
		failures = append(failures, fmt.Errorf("search returned %d issues for excluded property values before metadata changes", c.MetadataPresentBefore))
	}
	if c.MetadataNotFound > 0 {
		failures = append(failures, fmt.Errorf("search did not return %d issues for property values that a metadata change included", c.MetadataNotFound))
	}
	if c.Moves < c.MovesPlanned {
		failures = append(failures, fmt.Errorf("the run committed %d of %d planned moves", c.Moves, c.MovesPlanned))
	}
	if c.MovesUnverified > 0 {
		failures = append(failures, fmt.Errorf("the command could not verify that search returned %d moved issues only under their new workspaces", c.MovesUnverified))
	}
	return failures
}
