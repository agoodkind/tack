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
func (s SearchMixedSummary) Verify(ctx context.Context) error {
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
	// A dry run does not measure a window. A stopped run skips the catch-up wait.
	// At the stop, the probe counts every pending marker as not found and every pending
	// deletion as unverified. After a measured run that did not stop, Verify fails for
	// incomplete catch-up, markers not found, and deletions not verified.
	if s.Measured && !s.Stopped {
		if !s.CatchUp.Completed {
			failures = append(failures, fmt.Errorf("live, access, or cleanup search work with enqueue times before the end of writes remained pending after %s of catch-up", s.CatchUp.Elapsed))
		}
		add(s.MarkersNotFound > 0, "search did not return %d written issues for their marker words", s.MarkersNotFound)
		add(s.DeletesUnverified > 0, "the command could not verify the removal of %d deleted issues from search results", s.DeletesUnverified)
	}
	if len(failures) == 0 {
		return nil
	}
	joined := errors.Join(failures...)
	slog.ErrorContext(ctx, "qa.datagen.search_mixed_checks_failed", slog.String("err", joined.Error()))
	return joined
}
