package datagen

import (
	"context"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
)

const (
	datagenActorName  = "qa datagen"
	expiredTokenLabel = "qa-datagen-expired"
	expiredTokenAge   = time.Hour
)

var datagenActorID = deterministicUUID("tack-qa-datagen-service")

func datagenOperation(ctx context.Context, verb audit.Verb, orgID uuid.UUID) audit.Event {
	actor := audit.Actor{
		Type: audit.ActorService, ID: datagenActorID, Email: "", Name: datagenActorName,
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
	return postgres.NewChangeOperation(ctx, verb, actor, orgID, audit.SourceSystem)
}
