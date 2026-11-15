package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rolloutStep verifies the rollout claim and stored state before applying step.
// The completion transaction also records the initiating command's final result.
func (s *SearchAccessRolloutStore) rolloutStep(
	ctx context.Context,
	work searchdomain.Work,
	read searchdomain.AccessRollout,
	step func(fdb.Transaction, *searchRolloutRecord, searchWorkRecord) error,
) error {
	return transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			workRecord, err := s.work.verifyClaim(ctx, tr, work)
			if err != nil {
				return err
			}

			record, err := readRollout(ctx, tr, work.OrgID)
			if err != nil {
				return err
			}

			if !record.matches(read) {
				return searchdomain.ErrWorkChanged
			}

			if err := step(tr, &record, workRecord); err != nil {
				return err
			}

			if record.Phase == string(searchdomain.AccessStable) {
				if err := appendRolloutResult(
					ctx,
					tr,
					record,
					s.work.clock.Now(),
					audit.OutcomeDetails{
						Outcome:        audit.OutcomeCompleted,
						FailureCode:    "",
						FailureMessage: "",
						CommittedSteps: nil,
					},
				); err != nil {
					return err
				}
			}

			return writeSearchRecord(ctx, tr, searchRolloutKey(work.OrgID), record)
		},
	)
}
