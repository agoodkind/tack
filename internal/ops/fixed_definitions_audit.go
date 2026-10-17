package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// fixedDefinitionFields stores the search declaration and feature list
// that an audit delta records for one property definition.
type fixedDefinitionFields struct {
	Search            *node.SearchProjection `json:"search"`
	AppliesToFeatures []string               `json:"applies_to_features"`
}

// stageFixedDefinitionAudit stages a property_def.update event with source
// migration on the context it returns. The delta records search and
// applies_to_features before and after the correction and lists the
// changed fields. The event ID uses a name-based UUID of the organization,
// the definition, and the corrected fields. A retry of the same correction
// uses the same event ID.
func stageFixedDefinitionAudit(ctx context.Context, before, after *node.PropertyDef) (context.Context, error) {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		return ctx, fixedDefinitionAuditFailure(ctx, after, errors.New("operator principal is missing"))
	}
	beforeJSON, err := json.Marshal(fixedDefinitionFields{Search: before.Search, AppliesToFeatures: before.AppliesToFeatures})
	if err != nil {
		return ctx, fixedDefinitionAuditFailure(ctx, after, err)
	}
	afterJSON, err := json.Marshal(fixedDefinitionFields{Search: after.Search, AppliesToFeatures: after.AppliesToFeatures})
	if err != nil {
		return ctx, fixedDefinitionAuditFailure(ctx, after, err)
	}
	event := audit.Event{
		Verb:    string(audit.VerbPropertyDefUpdate),
		EventID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("fixed-definition:"+after.OrgID.String()+":"+after.ID.String()+":"+string(afterJSON))),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "property_def", NodeType: "", ID: after.ID, Identifier: after.Name, Name: after.Name},
		Context: audit.EventContext{
			OrgID: after.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceMigration, Tool: fixedDefinitionsCommand, RPC: "", Reason: "",
		},
		Delta:   &audit.Delta{Before: beforeJSON, After: afterJSON, Changed: changedFixedFields(before, after)},
		Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "", OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return ctx, fixedDefinitionAuditFailure(ctx, after, err)
	}
	staged := auditintent.WithSlot(ctx, fixedDefinitionsCommand, principal.ID)
	if !auditintent.Stage(staged, payload) {
		return ctx, fixedDefinitionAuditFailure(ctx, after, errors.New("the audit slot refused the event"))
	}
	return staged, nil
}

// changedFixedFields returns the JSON names of the corrected fields that
// differ between before and after.
func changedFixedFields(before, after *node.PropertyDef) []string {
	changed := make([]string, 0, 2)
	beforeSearch, beforeErr := json.Marshal(before.Search)
	afterSearch, afterErr := json.Marshal(after.Search)
	if beforeErr != nil || afterErr != nil || string(beforeSearch) != string(afterSearch) {
		changed = append(changed, "search")
	}
	if !slices.Equal(before.AppliesToFeatures, after.AppliesToFeatures) {
		changed = append(changed, "applies_to_features")
	}
	return changed
}

func fixedDefinitionAuditFailure(ctx context.Context, definition *node.PropertyDef, err error) error {
	slog.ErrorContext(ctx, "metadata.fixed_definition.audit_failed", slog.String("err", err.Error()),
		slog.String("property_definition_id", definition.ID.String()))
	return fmt.Errorf("stage fixed definition audit for %s: %w", definition.ID, err)
}
