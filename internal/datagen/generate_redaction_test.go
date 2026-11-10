package datagen

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &output
}

func redactionIdentities() Identities {
	return Identities{Workspaces: []WorkspaceIdentity{{
		OrgID:  uuid.Must(uuid.NewV7()),
		Actors: []Actor{{UserID: uuid.Must(uuid.NewV7()), Email: "qa@example.invalid", Token: "token"}},
	}}}
}

func TestAuditRedactionDryRunPlansAndCallsNothing(t *testing.T) {
	output := captureLogs(t)
	generator := &Generator{
		driver: NewDriver(nil, true, 245), dryRun: true, redactAuditPII: true,
		identities: redactionIdentities(),
		redactActor: func(context.Context, uuid.UUID, uuid.UUID) (audit.ActorRedaction, error) {
			return audit.ActorRedaction{}, errors.New("a dry run must not redact")
		},
	}
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
	if !strings.Contains(output.String(), "qa.datagen.audit_redaction_planned") {
		t.Fatalf("dry-run logs = %q, want planned event", output.String())
	}
}

func TestAuditRedactionRunsTheHostPathForTheFirstActor(t *testing.T) {
	output := captureLogs(t)
	identities := redactionIdentities()
	var gotOrg, gotActor uuid.UUID
	generator := &Generator{
		driver: NewDriver(nil, false, 245), redactAuditPII: true, identities: identities,
		redactActor: func(_ context.Context, orgID, actorID uuid.UUID) (audit.ActorRedaction, error) {
			gotOrg, gotActor = orgID, actorID
			return audit.ActorRedaction{OrgID: orgID, ActorID: actorID, PIIRefCount: 3, Unredacted: 3, Redacted: 3}, nil
		},
	}
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
	workspace := identities.Workspaces[0]
	if gotOrg != workspace.OrgID || gotActor != workspace.Actors[0].UserID {
		t.Fatalf("redacted org %s actor %s, want org %s actor %s", gotOrg, gotActor, workspace.OrgID, workspace.Actors[0].UserID)
	}
	if !strings.Contains(output.String(), "qa.datagen.audit_redaction_requested") ||
		!strings.Contains(output.String(), "redacted=3") {
		t.Fatalf("logs = %q, want the requested event with its counts", output.String())
	}
}

func TestAuditRedactionFailsLoudlyWhenTheHostPathFails(t *testing.T) {
	captureLogs(t)
	generator := &Generator{
		driver: NewDriver(nil, false, 245), redactAuditPII: true, identities: redactionIdentities(),
		redactActor: func(context.Context, uuid.UUID, uuid.UUID) (audit.ActorRedaction, error) {
			return audit.ActorRedaction{}, errors.New("permission denied for table audit.pii")
		},
	}
	err := generator.redactOneActor(t.Context())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("redactOneActor() error = %v, want the redaction failure", err)
	}
}

func TestAuditRedactionSkipsWithoutDSNs(t *testing.T) {
	output := captureLogs(t)
	generator := &Generator{
		driver: NewDriver(nil, false, 245), redactAuditPII: true, identities: redactionIdentities(),
	}
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
	if !strings.Contains(output.String(), "qa.datagen.audit_redaction_skipped") {
		t.Fatalf("logs = %q, want the skipped event", output.String())
	}
}
