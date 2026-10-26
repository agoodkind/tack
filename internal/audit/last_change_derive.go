package audit

import (
	"slices"

	"github.com/google/uuid"
)

// legacyChangeVerbs lists the verbs of legacy rows that can change a node or
// one of its relationships.
var legacyChangeVerbs = []Verb{
	VerbNodeCreate, VerbNodeUpdate, VerbNodeDelete, VerbNodeReferenceRename,
	VerbRelationshipAdd, VerbRelationshipUpdate, VerbRelationshipRemove,
}

// legacyFailedOutcomes lists the outcomes of legacy rows that did not change
// anything.
var legacyFailedOutcomes = []Outcome{OutcomeError, OutcomeRefused, OutcomeFailed, OutcomePending}

// relationshipIdentity identifies one relationship.
type relationshipIdentity struct {
	sourceID     uuid.UUID
	relationType string
	targetID     uuid.UUID
}

// LastChangeDeriver derives a LastChangeReport for one node from ledger rows.
// Add accepts the rows in any order. The deriver stores the candidates of
// each entry, not the rows.
type LastChangeDeriver struct {
	nodeID        uuid.UUID
	path          string
	historyStart  bool
	fields        map[string]*changeCandidates
	recordPaths   map[string]bool
	relationships map[relationshipIdentity]*changeCandidates
	seenTargets   map[relationshipIdentity]bool
	legacy        []legacyEvidence
}

// NewLastChangeDeriver returns a deriver for nodeID. A non-empty path limits
// the report to that field path and leaves out relationships.
func NewLastChangeDeriver(nodeID uuid.UUID, path string) *LastChangeDeriver {
	return &LastChangeDeriver{
		nodeID: nodeID, path: path, historyStart: false,
		fields: map[string]*changeCandidates{}, recordPaths: map[string]bool{},
		relationships: map[relationshipIdentity]*changeCandidates{}, seenTargets: map[relationshipIdentity]bool{},
		legacy: nil,
	}
}

// Add records the evidence of one row. It ignores rows of other nodes, read
// rows, result rows, and unsuccessful changes. It returns an error when the
// record in a change row is not a JSON object.
func (deriver *LastChangeDeriver) Add(row Row) error {
	if row.Context.AuditFormatVersion == AuditFormatLegacy {
		deriver.addLegacy(row)
		return nil
	}
	target := row.Context.Target
	if row.Context.AuditFormatVersion != AuditFormatComplete || target == nil {
		return nil
	}
	if target.Kind == TargetKindRelationship && (target.SourceID == deriver.nodeID || target.TargetID == deriver.nodeID) {
		identity := relationshipIdentity{sourceID: target.SourceID, relationType: target.RelationType, targetID: target.TargetID}
		deriver.seenTargets[identity] = true
		if successfulChange(row) {
			deriver.addRelationshipChange(identity, row)
		}
		return nil
	}
	if target.Kind == TargetKindNode && target.ID == deriver.nodeID && successfulChange(row) {
		return deriver.addNodeChange(row)
	}
	return nil
}

// successfulChange reports whether row records one committed record change.
func successfulChange(row Row) bool {
	succeeded := row.Outcome == OutcomeCompleted || row.Outcome == OutcomeOK
	return succeeded && row.Context.Phase == PhaseChange && row.Delta != nil
}

func (deriver *LastChangeDeriver) addLegacy(row Row) {
	if !slices.Contains(legacyChangeVerbs, Verb(row.Action)) || slices.Contains(legacyFailedOutcomes, row.Outcome) {
		return
	}
	if row.EntityID != deriver.nodeID {
		return
	}
	if Verb(row.Action) == VerbNodeCreate {
		deriver.historyStart = true
	}
	deriver.legacy = append(deriver.legacy, legacyEvidence{change: rowChange(row, LastChangeSet)})
}

func (deriver *LastChangeDeriver) addRelationshipChange(identity relationshipIdentity, row Row) {
	effect := LastChangeSet
	if len(row.Delta.After) == 0 {
		effect = LastChangeRemoved
	}
	candidates, ok := deriver.relationships[identity]
	if !ok {
		candidates = &changeCandidates{versioned: nil, unversioned: nil}
		deriver.relationships[identity] = candidates
	}
	candidates.add(rowChange(row, effect))
}

func (deriver *LastChangeDeriver) addNodeChange(row Row) error {
	if len(row.Delta.Before) == 0 {
		deriver.historyStart = true
	}
	before, err := recordPointers(row.Delta.Before)
	if err != nil {
		return err
	}
	after, err := recordPointers(row.Delta.After)
	if err != nil {
		return err
	}
	for path := range before {
		deriver.recordPaths[path] = true
	}
	for path := range after {
		deriver.recordPaths[path] = true
	}
	for _, path := range row.Delta.Changed {
		effect := LastChangeSet
		if before[path] && !after[path] {
			effect = LastChangeRemoved
		}
		candidates, ok := deriver.fields[path]
		if !ok {
			candidates = &changeCandidates{versioned: nil, unversioned: nil}
			deriver.fields[path] = candidates
		}
		candidates.add(rowChange(row, effect))
	}
	return nil
}

// recordPointers returns the set of field paths in one record encoding.
func recordPointers(encoding []byte) (map[string]bool, error) {
	fields, err := recordFields(encoding)
	if err != nil {
		return nil, err
	}
	pointers := make(map[string]bool, len(fields))
	for field := range fields {
		pointers[field.pointer()] = true
	}
	return pointers, nil
}

func rowChange(row Row, effect LastChangeEffect) LastChange {
	return LastChange{
		EventID: row.EventID, Action: row.Action, Outcome: row.Outcome, ActorID: row.ActorID,
		ActorKind: row.ActorKind, EventTime: row.EventTime, CommitVersion: row.Context.CommitVersion, Effect: effect,
	}
}
