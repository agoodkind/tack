package ops

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/service"
)

func TestReferenceRepairBackfillDerivesBothProductionSeedRuns(t *testing.T) {
	orgID := uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e")
	classes, err := deriveSeedClasses(t.Context(), audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")}, orgID, time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("derive seed events: %v", err)
	}
	if len(classes) != 2 || classes[0].Count.Derived != 2 || classes[0].Count.Recorded != 2 || classes[1].Count.Derived != 50 || classes[1].Count.Recorded != 50 {
		t.Fatalf("seed classes = %+v", classes)
	}
	nodeTypes, propertyDefs := service.DefaultOrgDefinitions(orgID)
	for _, testCase := range []struct {
		name           string
		eventIndex     int
		wantID         uuid.UUID
		wantIdentifier string
	}{
		{name: "node type", eventIndex: 0, wantID: nodeTypes[0].ID, wantIdentifier: nodeTypes[0].Slug},
		{name: "property definition", eventIndex: len(nodeTypes), wantID: propertyDefs[0].ID, wantIdentifier: propertyDefs[0].Name},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			event := classes[1].Events[testCase.eventIndex]
			if event.Entity.ID != testCase.wantID || event.Entity.Identifier != testCase.wantIdentifier {
				t.Fatalf("entity = %+v", event.Entity)
			}
			var extra reconstructionExtra
			if err := json.Unmarshal(event.Extra, &extra); err != nil {
				t.Fatalf("decode seed definition extra: %v", err)
			}
			if !extra.HistoricalDefinitionSetUnproven || extra.ObservedSeedDefinition == nil {
				t.Fatalf("seed definition evidence = %+v", extra)
			}
		})
	}
}
