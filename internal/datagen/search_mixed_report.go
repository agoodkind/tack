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
	// Verify skips these checks for a dry run. For every other stopped run, it reports the stop
	// reason, whether the run stopped during setup or during the measured window.
	// For measured runs that finish, Verify rejects incomplete catch-up, markers that search
	// did not return, and deletions that the prober could not verify.
	// For every measured run, stopped or finished, Verify also rejects markers that the final
	// probe pass did not search successfully.
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
	add(s.Measured && s.MarkersUnsearched > 0, "the final probe pass did not successfully search for %d written issue markers", s.MarkersUnsearched)
	if len(failures) == 0 {
		return nil
	}
	joined := errors.Join(failures...)
	slog.ErrorContext(ctx, "qa.datagen.search_mixed_checks_failed", slog.String("err", joined.Error()))
	return joined
}

// SearchMixedChanges reports planned and committed metadata changes and moves.
// It also reports counts for active page text checks before metadata changes and metadata
// and move checks still pending when probing stops.
type SearchMixedChanges struct {
	MetadataPlanned  int
	MetadataChanges  int
	MetadataNotFound int
	MovesPlanned     int
	Moves            int
	MovesUnverified  int
	// MetadataAbsentBefore counts checks that read at least one active page from the serving
	// index before the metadata change and do not find the excluded property value in any page text.
	MetadataAbsentBefore int
	// MetadataPresentBefore counts checks that find the excluded property value in active page
	// text from the serving index before the metadata change.
	MetadataPresentBefore int
	// MetadataUncheckedBefore counts checks that fail to read active pages from the serving
	// index or find that the issue does not have any active pages before the metadata change.
	MetadataUncheckedBefore int
}

// failures returns errors when the run commits fewer metadata changes or moves than planned,
// when active page text contains an excluded value before its metadata change, or when a
// metadata or move check remains pending when probing stops.
func (c SearchMixedChanges) failures() []error {
	failures := make([]error, 0)
	if c.MetadataChanges < c.MetadataPlanned {
		failures = append(failures, fmt.Errorf("the run committed %d of %d planned metadata changes", c.MetadataChanges, c.MetadataPlanned))
	}
	if c.MetadataPresentBefore > 0 {
		failures = append(failures, fmt.Errorf("active page text contained excluded property values for %d issues before metadata changes", c.MetadataPresentBefore))
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
