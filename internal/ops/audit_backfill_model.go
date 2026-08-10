package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

const (
	referenceRepairDate          = "2026-08-07"
	referenceRepairFollowupDate  = "2026-08-08"
	referenceRepairEvidence      = "TACK-342"
	productionSeedEmail          = "alex@goodkind.io"
	productionSeedOrgSlug        = "goodkind-io"
	productionSeedWorkspaceSlug  = "main"
	recordedReferenceRenames     = 104
	recordedCounterSeeds         = 11
	recordedReferenceKeys        = 1620
	recordedFollowupReferenceKey = 1
	recordedSeedRuns             = 2
	recordedSeedDefinitions      = 50
	followupOldReference         = "TACK-403"
	followupNewReference         = "TACK-420"
)

// referenceRepairStart is when the 2026-08-07 repair began, 21:31 Pacific,
// expressed in UTC because the store records creation times in UTC. Every
// derivation is bounded by it: a node created after the repair ran cannot
// have had its reference key written by that repair, and counting it would
// inflate the reconstruction with history that never happened.
var referenceRepairStart = time.Date(2026, time.August, 8, 4, 31, 0, 0, time.UTC)

var referenceRepairBackfillNamespace = uuid.MustParse("5eb28a3b-920d-5302-9c81-0f8db0b0b017")

type auditBackfillCount struct {
	Class    string `json:"class"`
	Derived  int    `json:"derived"`
	Recorded int    `json:"recorded"`
}

type auditBackfillClass struct {
	Count  auditBackfillCount
	Events []audit.Event
}

type auditBackfillPlan struct {
	Classes []auditBackfillClass
}

type reconstructionExtra struct {
	Class                              string                  `json:"class"`
	Reconstruction                     bool                    `json:"reconstruction"`
	HistoricalTime                     string                  `json:"historical_time"`
	Evidence                           string                  `json:"evidence"`
	Scope                              string                  `json:"scope,omitempty"`
	Template                           string                  `json:"template,omitempty"`
	Run                                string                  `json:"run,omitempty"`
	SeedEmail                          string                  `json:"seed_email,omitempty"`
	OrgSlug                            string                  `json:"org_slug,omitempty"`
	WorkspaceSlug                      string                  `json:"workspace_slug,omitempty"`
	HistoricalReferenceKey             string                  `json:"historical_reference_key,omitempty"`
	HistoricalReferenceKeyTextUnproven bool                    `json:"historical_reference_key_text_unproven,omitempty"`
	ObservedReferenceKey               string                  `json:"observed_reference_key_at_reconstruction,omitempty"`
	ObservedReferenceTemplate          string                  `json:"observed_reference_template_at_reconstruction,omitempty"`
	HistoricalDefinitionSetUnproven    bool                    `json:"historical_definition_set_unproven,omitempty"`
	ObservedSeedDefinition             *observedSeedDefinition `json:"observed_seed_definition_at_reconstruction,omitempty"`
}

type observedSeedDefinition struct {
	Type       string    `json:"type"`
	ID         uuid.UUID `json:"id"`
	NodeType   string    `json:"node_type,omitempty"`
	Identifier string    `json:"identifier,omitempty"`
	Name       string    `json:"name,omitempty"`
}

type historicalSeedRun struct {
	Name string
	Date string
}

var historicalSeedRuns = []historicalSeedRun{
	{Name: "production-2026-08-07", Date: referenceRepairDate},
	{Name: "production-2026-08-08", Date: referenceRepairFollowupDate},
}

func (p auditBackfillPlan) Counts() []auditBackfillCount {
	counts := make([]auditBackfillCount, 0, len(p.Classes))
	for _, class := range p.Classes {
		counts = append(counts, class.Count)
	}
	return counts
}

func (p auditBackfillPlan) Validate(ctx context.Context) error {
	for _, class := range p.Classes {
		if class.Count.Derived == class.Count.Recorded {
			continue
		}
		err := fmt.Errorf("%s reconstruction count mismatch: derived %d, recorded %d",
			class.Count.Class, class.Count.Derived, class.Count.Recorded)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.count_mismatch", slog.String("err", err.Error()))
		return err
	}
	return nil
}

func (p auditBackfillPlan) Write(ctx context.Context, outbox audit.IdempotentOutboxWriter) error {
	if err := p.Validate(ctx); err != nil {
		return err
	}
	for _, class := range p.Classes {
		for _, event := range class.Events {
			if _, err := outbox.WriteOutboxIfAbsent(ctx, event); err != nil {
				wrapped := fmt.Errorf("record reconstructed %s event %s: %w", class.Count.Class, event.EventID, err)
				telemetry.L(ctx).Error("audit.reference_repair_backfill.write_failed",
					slog.String("class", class.Count.Class), slog.String("err", wrapped.Error()))
				return wrapped
			}
		}
	}
	return nil
}

func reconstructionEvent(
	ctx context.Context,
	verb audit.Verb,
	principal audit.OperatorPrincipal,
	entity audit.Entity,
	orgID uuid.UUID,
	idempotencyKey string,
	extra reconstructionExtra,
	occurredAt time.Time,
) (audit.Event, error) {
	encodedExtra, err := json.Marshal(extra)
	if err != nil {
		wrapped := fmt.Errorf("encode reconstructed %s event: %w", extra.Class, err)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.extra_encode_failed", slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	return audit.Event{
		Verb:    string(verb),
		EventID: uuid.NewSHA1(referenceRepairBackfillNamespace, []byte(idempotencyKey)),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: entity,
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: idempotencyKey,
		OccurredAt: occurredAt.UTC(), Extra: encodedExtra,
	}, nil
}
