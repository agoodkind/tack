package ops

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestReferenceRenameIndexIsOrderIndependent(t *testing.T) {
	nodeID := uuid.MustParse("019ea37c-c572-7eeb-b3ee-0116e44d5118")
	first := referenceRenameEvidence{OldReference: "TACK-18", NewReference: followupOldReference, NodeID: nodeID.String()}
	second := referenceRenameEvidence{OldReference: followupOldReference, NewReference: followupNewReference, NodeID: nodeID.String()}
	for _, testCase := range []struct {
		name    string
		records []referenceRenameEvidence
	}{
		{name: "chronological", records: []referenceRenameEvidence{first, second}},
		{name: "reversed", records: []referenceRenameEvidence{second, first}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			byNode, err := indexReferenceRenameEvidence(t.Context(), testCase.records)
			if err != nil {
				t.Fatalf("index evidence: %v", err)
			}
			if got := byNode[nodeID].NewReference; got != followupNewReference {
				t.Fatalf("indexed reference = %q, want %q", got, followupNewReference)
			}
		})
	}
}

func TestReferenceRenameUnchainedPairIsRefused(t *testing.T) {
	nodeID := uuid.MustParse("019ea37c-c572-7eeb-b3ee-0116e44d5118").String()
	_, err := indexReferenceRenameEvidence(t.Context(), []referenceRenameEvidence{{OldReference: "TACK-18", NewReference: followupOldReference, NodeID: nodeID}, {OldReference: "TACK-900", NewReference: "TACK-901", NodeID: nodeID}})
	if err == nil || !strings.Contains(err.Error(), "does not form a chain") {
		t.Fatalf("error = %v, want broken chain", err)
	}
}

func TestReferenceRenameEvidenceResolvesTheDoubleRenamedNode(t *testing.T) {
	byNode, err := referenceRenameEvidenceByNode(t.Context())
	if err != nil {
		t.Fatalf("index evidence: %v", err)
	}
	doubled := uuid.MustParse("019ea37c-c572-7eeb-b3ee-0116e44d5118")
	if len(byNode) != 103 || byNode[doubled].NewReference != followupNewReference {
		t.Fatalf("indexed evidence = %d records, doubled = %+v", len(byNode), byNode[doubled])
	}
}
