package main

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditRedactActorInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Org                 string
	ActorID             string
}

// auditRedactActorOp declares `audit redact-actor`: GDPR erasure of the PII
// payload behind every event one org recorded for one actor. The global
// --execute is the only action gate: without it the command reports what it
// would erase and records nothing; with it the choke-point records intent
// and outcome under audit.pii_redacted around the erasure.
func auditRedactActorOp(f *cli.Factory) clispec.Operation[auditRedactActorInput] {
	return clispec.Operation[auditRedactActorInput]{
		Name:    clispec.Name{Canonical: "redact-actor", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbAuditPIIRedacted), Mutates: true},
		Group:   auditGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Erase the audit PII payloads of one actor within one org",
		Long: "Prints how many audit.pii rows would be erased and changes nothing until " +
			"--execute is passed. The ledger rows and their hash chain stay intact; only " +
			"the actor's PII payload becomes null. Events the actor left in other orgs " +
			"are untouched.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditRedactActorInput]{
			clispec.StringParam("org", "org_id (UUID)", "", true, func(in *auditRedactActorInput, v string) { in.Org = v }),
			clispec.StringParam("actor-id", "actor_id (UUID) whose PII payloads to erase", "", true, func(in *auditRedactActorInput, v string) { in.ActorID = v }),
		},
		New: func() auditRedactActorInput {
			return auditRedactActorInput{InputMarker: clispec.InputMarker{}, Org: "", ActorID: ""}
		},
		DryRun: func(ctx context.Context, in auditRedactActorInput, sink clispec.ResultSink) error {
			return runAuditRedactActor(ctx, f, in, sink, false)
		},
		Run: func(ctx context.Context, in auditRedactActorInput, sink clispec.ResultSink) error {
			return runAuditRedactActor(ctx, f, in, sink, true)
		},
	}
}

func runAuditRedactActor(ctx context.Context, f *cli.Factory, in auditRedactActorInput, sink clispec.ResultSink, apply bool) error {
	orgID, err := parseAuditUUID(ctx, "audit redact-actor", "--org", in.Org)
	if err != nil {
		return err
	}
	actorID, err := parseAuditUUID(ctx, "audit redact-actor", "--actor-id", in.ActorID)
	if err != nil {
		return err
	}
	reader, err := openAuditReader(ctx, f, "audit redact-actor")
	if err != nil {
		return err
	}
	defer reader.Close()
	redactor, err := audit.NewRedactor(ctx, f.Cfg.AuditRedactorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.redactor_open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit redact-actor: redactor: %w", err)
	}
	defer redactor.Close()

	var redaction audit.ActorRedaction
	if apply {
		redaction, err = audit.RedactActorInOrg(ctx, reader, redactor, orgID, actorID)
	} else {
		redaction, err = audit.PlanActorRedaction(ctx, reader, redactor, orgID, actorID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.redact_actor_failed",
			slog.String("org_id", orgID.String()), slog.String("actor_id", actorID.String()),
			slog.Bool("apply", apply), slog.String("err", err.Error()))
		return fmt.Errorf("audit redact-actor: %w", err)
	}
	if err := clispec.WriteJSONValue(ctx, sink, newAuditRedactActorResult(redaction, !apply)); err != nil {
		slog.ErrorContext(ctx, "audit.redact_actor_render_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit redact-actor: render report: %w", err)
	}
	return nil
}

func newAuditRedactActorResult(redaction audit.ActorRedaction, dryRun bool) auditRedactActorResult {
	return auditRedactActorResult{
		Command: "audit.redact_actor", DryRun: dryRun,
		OrgID: redaction.OrgID, ActorID: redaction.ActorID,
		PIIRefCount: redaction.PIIRefCount, Unredacted: redaction.Unredacted, Redacted: redaction.Redacted,
	}
}
