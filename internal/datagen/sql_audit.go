package datagen

import (
	"context"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
)

// datagenActorName sets the QA audit service actor's name; expiredTokenLabel
// and expiredTokenAge set the expired token's label and age.
const (
	datagenActorName  = "qa datagen"
	expiredTokenLabel = "qa-datagen-expired"
	expiredTokenAge   = time.Hour
)

// datagenActorID supplies a fixed deterministic ID for the QA service actor.
var datagenActorID = deterministicUUID("tack-qa-datagen-service")

// datagenOperation builds a repository change operation for the QA service
// actor with the supplied verb and orgID and audit.SourceSystem as the source.
func datagenOperation(ctx context.Context, verb audit.Verb, orgID uuid.UUID) audit.Event {
	actor := audit.Actor{
		Type: audit.ActorService, ID: datagenActorID, Email: "", Name: datagenActorName,
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
	return postgres.NewChangeOperation(ctx, verb, actor, orgID, audit.SourceSystem)
}
