package ops

import (
	"fmt"

	"github.com/google/uuid"
)

type restoredLedgerExportEmptyError struct {
	OrgID uuid.UUID
}

func (failure restoredLedgerExportEmptyError) Error() string {
	return fmt.Sprintf(
		"org %s: the restored ledger lists this org but the export did not write rows",
		failure.OrgID,
	)
}

type restoredLedgerExportCountMismatchError struct {
	OrgID    uuid.UUID
	Exported int
	Restored int
}

func (failure restoredLedgerExportCountMismatchError) Error() string {
	return fmt.Sprintf(
		"org %s: the export wrote %d of the %d rows in the restored ledger; "+
			"omitted rows lack chain verification",
		failure.OrgID,
		failure.Exported,
		failure.Restored,
	)
}

type restoredLedgerSequenceGapError struct {
	OrgID uuid.UUID
	Gaps  int
}

func (failure restoredLedgerSequenceGapError) Error() string {
	return fmt.Sprintf(
		"org %s: %d sequence gap(s) in a whole-ledger export; rows are missing from "+
			"inside the chain and the links across them are unverified",
		failure.OrgID,
		failure.Gaps,
	)
}
