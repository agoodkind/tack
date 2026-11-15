package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRollbackRecordsOriginalResults verifies the original rollout and
// rollback results before activation and after a committed activation.
func TestSearchRollbackRecordsOriginalResults(t *testing.T) {
	for _, activated := range []bool{false, true} {
		name := "before activation"
		if activated {
			name = "after activation"
		}

		t.Run(
			name,
			func(t *testing.T) {
				fixture := newParentEdgeFixture(t)
				fixture.createLeaf(t, fixture.firstEntry, "amber rollback leaf")
				fixture.drain(t)
				authority := fixture.kind.orgID.String()
				if _, err := fixture.runOps(
					t,
					"ops",
					"search",
					"access-rollout",
					"--authority",
					authority,
					"--candidate",
					searchaccess.RotatedVersion,
				); err != nil {
					t.Fatalf("start public access rollout: %v", err)
				}

				original := rolloutInitiation(t, fixture)
				if activated {
					runRequestedRolloutUntilActivated(t, fixture)
				}

				if _, err := fixture.runOps(
					t,
					"ops",
					"search",
					"access-rollback",
					"--authority",
					authority,
				); err != nil {
					t.Fatalf("run public access rollback: %v", err)
				}

				rollback := rolloutInitiation(t, fixture)
				requireCommandInitiation(t, fixture, audit.VerbOpsSearchAccessRollback, rollback)
				runRolloutUntilStable(t, fixture)
				stack := requirePermanentSearchResults(t, rollback)
				expected := audit.OutcomeIncomplete
				if activated {
					expected = audit.OutcomePartiallyCompleted
				}

				requirePermanentSearchResult(t, stack, original, expected)
			},
		)
	}
}

func runRequestedRolloutUntilActivated(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	rollouts := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet())

	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		current, err := rollouts.Current(
			integration.WithTestOperation(t, t.Context()),
			fixture.kind.orgID,
		)
		if err != nil {
			t.Fatalf("read requested access rollout: %v", err)
		}

		if current.Activated() && current.Phase == searchdomain.AccessVerifying {
			return
		}

		runSliceOrWait(t, fixture)
	}

	t.Fatalf("requested rollout did not activate within %s", replacementDeadline)
}
