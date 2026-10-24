package integration

import (
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/service"
)

// LedgerResumeAudit returns a delete resumer recording that writes to the
// ledger operator outbox through env.Ops.Pool. A test that reads every
// FoundationDB outbox event after a resume pass uses it, and the resume run
// events stay out of that outbox.
func (env *TestEnv) LedgerResumeAudit() service.JobAudit {
	return service.JobAudit{
		Outbox: audit.NewPoolOutbox(env.Ops.Pool),
		Actor:  audit.ServiceActor(uuid.Must(uuid.NewV7()), "tack-test-delete-resumer"),
	}
}
