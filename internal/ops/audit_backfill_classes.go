package ops

import (
	"context"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func deriveCounterSeedClass(
	ctx context.Context,
	env *Env,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	repairStart time.Time,
) (auditBackfillClass, error) {
	counters, err := enumerateReferenceCounters(ctx, env, orgID, &repairStart)
	if err != nil {
		return auditBackfillClass{}, err
	}
	events := make([]audit.Event, 0, len(counters))
	for _, counter := range counters {
		event, eventErr := reconstructionEvent(
			ctx,
			audit.VerbOpsRepairReferenceUniqueness,
			principal,
			audit.Entity{Type: "sequence_counter", NodeType: "", ID: uuid.NewSHA1(referenceRepairBackfillNamespace, []byte(counter.Key)), Identifier: counter.Key, Name: ""},
			orgID,
			"tack-429-counter:"+counter.Key,
			reconstructionExtra{
				Class: "counter seeds", Reconstruction: true, HistoricalTime: referenceRepairDate,
				Evidence: referenceRepairEvidence, Scope: counter.Key, Template: "", Run: "",
				SeedEmail: "", OrgSlug: "", WorkspaceSlug: "", HistoricalReferenceKey: "",
				HistoricalReferenceKeyTextUnproven: false, ObservedReferenceKey: "",
				ObservedReferenceTemplate: "", HistoricalDefinitionSetUnproven: false,
				ObservedSeedDefinition: nil, SubjectDeletedAt: "", SubjectDeletionEventID: "",
				SubjectIdentityUnrecorded: false,
			},
			occurredAt,
		)
		if eventErr != nil {
			return auditBackfillClass{}, eventErr
		}
		events = append(events, event)
	}
	return auditBackfillClass{Count: auditBackfillCount{Class: "counter seeds", Derived: len(events), Recorded: recordedCounterSeeds, DeletedSubjects: 0, AbsentSubjects: 0}, Events: events}, nil
}

func deriveSeedClasses(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
) ([]auditBackfillClass, error) {
	nodeTypes, propertyDefs := service.DefaultOrgDefinitions(orgID)
	runClass := auditBackfillClass{Count: auditBackfillCount{Class: "seed runs", Derived: 0, Recorded: recordedSeedRuns, DeletedSubjects: 0, AbsentSubjects: 0}, Events: nil}
	definitionClass := auditBackfillClass{Count: auditBackfillCount{Class: "seed definitions", Derived: 0, Recorded: recordedSeedDefinitions, DeletedSubjects: 0, AbsentSubjects: 0}, Events: nil}
	for _, run := range historicalSeedRuns {
		runEvent, err := reconstructionEvent(
			ctx,
			audit.VerbBootstrapSeed,
			principal,
			audit.Entity{Type: "bootstrap_seed", NodeType: "", ID: uuid.NewSHA1(referenceRepairBackfillNamespace, []byte(run.Name)), Identifier: productionSeedEmail, Name: ""},
			orgID,
			"tack-429-seed-run:"+run.Name,
			reconstructionExtra{
				Class: "seed runs", Reconstruction: true, HistoricalTime: run.Date, Evidence: referenceRepairEvidence,
				Scope: "", Template: "", Run: run.Name, SeedEmail: productionSeedEmail,
				OrgSlug: productionSeedOrgSlug, WorkspaceSlug: productionSeedWorkspaceSlug,
				HistoricalReferenceKey: "", HistoricalReferenceKeyTextUnproven: false,
				ObservedReferenceKey: "", ObservedReferenceTemplate: "",
				HistoricalDefinitionSetUnproven: false, ObservedSeedDefinition: nil,
				SubjectDeletedAt: "", SubjectDeletionEventID: "", SubjectIdentityUnrecorded: false,
			},
			occurredAt,
		)
		if err != nil {
			return nil, err
		}
		runClass.Events = append(runClass.Events, runEvent)
		for _, nodeType := range nodeTypes {
			event, eventErr := seedNodeTypeEvent(ctx, principal, orgID, occurredAt, run, nodeType)
			if eventErr != nil {
				return nil, eventErr
			}
			definitionClass.Events = append(definitionClass.Events, event)
		}
		for _, propertyDef := range propertyDefs {
			event, eventErr := seedPropertyDefEvent(ctx, principal, orgID, occurredAt, run, propertyDef)
			if eventErr != nil {
				return nil, eventErr
			}
			definitionClass.Events = append(definitionClass.Events, event)
		}
	}
	runClass.Count.Derived = len(runClass.Events)
	definitionClass.Count.Derived = len(definitionClass.Events)
	return []auditBackfillClass{runClass, definitionClass}, nil
}

func seedNodeTypeEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	run historicalSeedRun,
	nodeType *node.NodeType,
) (audit.Event, error) {
	observed := observedSeedDefinition{
		Type: "node_type", ID: nodeType.ID, NodeType: nodeType.TypeKey,
		Identifier: nodeType.Slug, Name: nodeType.Name,
	}
	return reconstructionEvent(
		ctx,
		audit.VerbBootstrapSeed,
		principal,
		audit.Entity{Type: "seed_definition", NodeType: "", ID: nodeType.ID, Identifier: nodeType.Slug, Name: ""},
		orgID,
		"tack-429-seed-node-type:"+run.Name+":"+nodeType.ID.String(),
		seedDefinitionExtra(run, &observed),
		occurredAt,
	)
}

func seedPropertyDefEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	run historicalSeedRun,
	propertyDef *node.PropertyDef,
) (audit.Event, error) {
	observed := observedSeedDefinition{
		Type: "property_def", ID: propertyDef.ID, NodeType: "",
		Identifier: propertyDef.Name, Name: propertyDef.Name,
	}
	return reconstructionEvent(
		ctx,
		audit.VerbBootstrapSeed,
		principal,
		audit.Entity{Type: "seed_definition", NodeType: "", ID: propertyDef.ID, Identifier: propertyDef.Name, Name: ""},
		orgID,
		"tack-429-seed-property-def:"+run.Name+":"+propertyDef.ID.String(),
		seedDefinitionExtra(run, &observed),
		occurredAt,
	)
}

func seedDefinitionExtra(run historicalSeedRun, observed *observedSeedDefinition) reconstructionExtra {
	// The seed record proves each run applied 25 definitions, but it does not
	// preserve their identities or text. These values are observed during
	// reconstruction so readers cannot mistake them for the historical set.
	return reconstructionExtra{
		Class: "seed definitions", Reconstruction: true, HistoricalTime: run.Date,
		Evidence: referenceRepairEvidence, Scope: "", Template: "", Run: run.Name,
		SeedEmail: "", OrgSlug: "", WorkspaceSlug: "", HistoricalReferenceKey: "",
		HistoricalReferenceKeyTextUnproven: false, ObservedReferenceKey: "",
		ObservedReferenceTemplate: "", HistoricalDefinitionSetUnproven: true, ObservedSeedDefinition: observed,
		SubjectDeletedAt: "", SubjectDeletionEventID: "", SubjectIdentityUnrecorded: false,
	}
}
