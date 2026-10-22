package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
)

// authTokenRecordTimeout limits each token reconciliation that uses
// [context.WithoutCancel].
const authTokenRecordTimeout = 30 * time.Second

// authTokenOrg returns the user's sole org, or the system org when the user
// belongs to zero or several orgs. The system org satisfies the ledger reader's
// requirement for an org other than [uuid.Nil].
func authTokenOrg(ctx context.Context, members org.MemberRepository, userID uuid.UUID) (uuid.UUID, error) {
	orgIDs, err := members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.org_lookup_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("list orgs for user %s: %w", userID, err)
	}
	if len(orgIDs) == 1 {
		return orgIDs[0], nil
	}
	return audit.SystemOrgID(), nil
}

// authTokenOperation builds a repository change operation with principal as the
// audit actor and audit.SourceSystem as the source.
func authTokenOperation(verb audit.Verb, principal audit.OperatorPrincipal, orgID uuid.UUID) audit.Event {
	return postgres.NewChangeOperation(verb, postgres.OperatorActor(principal), orgID, audit.SourceSystem)
}
