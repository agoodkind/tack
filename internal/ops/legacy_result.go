package ops

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clispec"
)

func newLegacyLedgerRowResult(
	input datagenLegacyLedgerRowInput,
	orgID uuid.UUID,
) datagenLegacyLedgerRowResult {
	return datagenLegacyLedgerRowResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.qa.datagen.legacy-ledger-row",
		DryRun:       !input.Commit,
		OrgID:        orgID.String(),
		EventID:      "",
		Shard:        0,
		Seq:          0,
		EventTime:    "",
		Action:       input.Action,
		Tool:         input.Tool,
		DeletedNode:  "",
	}
}
