package ops

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

func TestReferenceRepairReferenceKeyLabelsObservedValues(t *testing.T) {
	orgID := uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e")
	for _, testCase := range []struct {
		name                               string
		rename                             referenceRenameEvidence
		wantHistoricalKey, wantObservedKey string
		wantHistoricalProof                bool
	}{
		{name: "key changed after repair is observed at reconstruction", wantObservedKey: "TACK-999"},
		{name: "rename evidence proves historical key", rename: referenceRenameEvidence{NodeID: "019ff316-c50e-74a8-a6b9-064f1a2f9237", NewReference: followupNewReference}, wantHistoricalKey: followupNewReference, wantHistoricalProof: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			nodeID := uuid.MustParse("019ff316-c50e-74a8-a6b9-064f1a2f9237")
			if testCase.rename.NodeID == "" {
				nodeID = uuid.MustParse("019ff316-c50e-74a8-a6b9-064f1a2f9236")
			}
			key := repairedReferenceKey{View: &node.NodeView{ID: nodeID, NodeType: "issue", Name: "Current title"}, Key: node.ReferenceKey{TemplateName: "reference", Encoded: "TACK-999"}}
			event, err := referenceKeyEvent(t.Context(), audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")}, orgID, time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC), key, testCase.rename, referenceRepairDate)
			if err != nil {
				t.Fatalf("build reference key event: %v", err)
			}
			var extra reconstructionExtra
			if err := json.Unmarshal(event.Extra, &extra); err != nil {
				t.Fatalf("decode reference key extra: %v", err)
			}
			if extra.HistoricalReferenceKey != testCase.wantHistoricalKey || extra.ObservedReferenceKey != testCase.wantObservedKey {
				t.Fatalf("extra = %+v", extra)
			}
			if !extra.HistoricalReferenceKeyTextUnproven != testCase.wantHistoricalProof || event.Entity.Identifier != testCase.wantHistoricalKey {
				t.Fatalf("historical evidence = %+v, entity = %+v", extra, event.Entity)
			}
			if event.Entity.NodeType != key.View.NodeType {
				t.Fatalf("entity node type = %q, want %q", event.Entity.NodeType, key.View.NodeType)
			}
		})
	}
}
