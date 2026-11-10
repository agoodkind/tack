package datagen

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// actorRedactorFor returns a function that redacts an actor using the
// configured reader and redactor DSNs. It returns nil when either DSN is empty.
func actorRedactorFor(cfg *config.Config) ActorRedactor {
	if cfg.AuditReaderDSN == "" || cfg.AuditRedactorDSN == "" {
		return nil
	}
	return func(ctx context.Context, orgID, actorID uuid.UUID) (audit.ActorRedaction, error) {
		reader, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
		if err != nil {
			return audit.ActorRedaction{}, loggedError(
				ctx,
				"qa datagen: open audit reader from AUDIT_READER_DSN",
				err,
			)
		}
		defer reader.Close()
		redactor, err := audit.NewRedactor(ctx, cfg.AuditRedactorDSN)
		if err != nil {
			return audit.ActorRedaction{}, loggedError(ctx, "qa datagen: open audit redactor", err)
		}
		defer redactor.Close()
		redaction, err := audit.RedactActorInOrg(ctx, reader, redactor, orgID, actorID)
		if err != nil {
			return audit.ActorRedaction{}, loggedError(ctx, "qa datagen: redact actor", err)
		}
		return redaction, nil
	}
}
