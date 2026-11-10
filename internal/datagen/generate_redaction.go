package datagen

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// ActorRedactor erases the audit PII one org recorded for one actor the way
// the `audit redact-actor` host command does, and reports what it found.
type ActorRedactor func(ctx context.Context, orgID, actorID uuid.UUID) (audit.ActorRedaction, error)

// redactOneActor exercises the host redaction path for the first generated
// actor of the first workspace. Redaction is an opt-in because it erases
// data the rest of the corpus and its audit rows refer to.
func (g *Generator) redactOneActor(ctx context.Context) error {
	if !g.redactAuditPII {
		logRedactionSkip(ctx, "explicit redaction opt-in is disabled")
		return nil
	}
	if len(g.identities.Workspaces) == 0 ||
		len(g.identities.Workspaces[0].Actors) == 0 {
		logRedactionSkip(ctx, "no generated actor is available")
		return nil
	}
	workspace := g.identities.Workspaces[0]
	actor := workspace.Actors[0]
	if g.dryRun {
		slog.InfoContext(ctx, "qa.datagen.audit_redaction_planned",
			slog.String("org_id", workspace.OrgID.String()),
			slog.String("actor_id", actor.UserID.String()),
		)
		return nil
	}
	if g.redactActor == nil {
		logRedactionSkip(ctx, "AUDIT_READER_DSN and AUDIT_REDACTOR_DSN are required")
		return nil
	}
	redaction, err := g.redactActor(ctx, workspace.OrgID, actor.UserID)
	if err != nil {
		return loggedError(ctx, "qa datagen: redact audit pii for "+actor.Email, err)
	}
	slog.InfoContext(ctx, "qa.datagen.audit_redaction_requested",
		slog.String("org_id", workspace.OrgID.String()),
		slog.String("actor_id", actor.UserID.String()),
		slog.Int("pii_ref_count", redaction.PIIRefCount),
		slog.Int64("unredacted_before", redaction.Unredacted),
		slog.Int64("redacted", redaction.Redacted),
	)
	return nil
}

func logRedactionSkip(ctx context.Context, reason string) {
	slog.InfoContext(ctx, "qa.datagen.audit_redaction_skipped", slog.String("reason", reason))
}
