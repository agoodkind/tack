package datagen

import (
	"context"
	"log/slog"
	"slices"
)

const auditRedactionTool = "tack_audit_redact_actor"

func (g *Generator) redactOneActor(ctx context.Context) error {
	if !g.redactAuditPII {
		logRedactionSkip(ctx, "explicit redaction opt-in is disabled", nil)
		return nil
	}
	if !g.synchronousAudit {
		logRedactionSkip(ctx, "synchronous audit writer is required", nil)
		return nil
	}
	if len(g.identities.Workspaces) == 0 ||
		len(g.identities.Workspaces[0].Actors) == 0 {
		logRedactionSkip(ctx, "no generated actor is available", nil)
		return nil
	}
	actor := g.identities.Workspaces[0].Actors[0]
	if !g.dryRun {
		names, err := g.driver.ListTools(ctx, actor.Token)
		if err != nil {
			logRedactionSkip(ctx, "tools/list failed", err)
			return nil
		}
		if !slices.Contains(names, auditRedactionTool) {
			logRedactionSkip(ctx, "redaction tool is not registered", nil)
			return nil
		}
	}
	_, err := g.driver.Call(ctx, actor.Token, auditRedactionTool, ToolArguments{
		ActorID: actor.UserID.String(),
	})
	if err != nil {
		if isToolUnavailable(err) {
			logRedactionSkip(ctx, "redaction tool became unavailable", err)
			return nil
		}
		logRedactionSkip(ctx, "redaction request failed", err)
		return nil
	}
	message := "qa.datagen.audit_redaction_requested"
	if g.dryRun {
		message = "qa.datagen.audit_redaction_planned"
	}
	slog.InfoContext(ctx, message)
	return nil
}

func logRedactionSkip(ctx context.Context, reason string, err error) {
	attributes := []slog.Attr{slog.String("reason", reason)}
	if err != nil {
		attributes = append(attributes, slog.String("err", err.Error()))
	}
	slog.LogAttrs(
		ctx,
		slog.LevelInfo,
		"qa.datagen.audit_redaction_skipped",
		attributes...,
	)
}
