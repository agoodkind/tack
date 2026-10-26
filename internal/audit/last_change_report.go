package audit

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Report returns the field entries in path order and the relationship
// entries in source, relation type, and target order. Pass truncated as true
// when the read stopped before the oldest row of the window. Entries without
// change evidence report partial when truncated is true or when the rows read
// do not include the node's creation.
func (deriver *LastChangeDeriver) Report(truncated bool) LastChangeReport {
	complete := deriver.historyStart && !truncated
	report := LastChangeReport{
		NodeID: deriver.nodeID, HistoryStartInWindow: deriver.historyStart, LegacyRowCount: len(deriver.legacy),
		Fields: []LastChangeEntry{}, Relationships: []LastChangeEntry{},
	}
	for _, path := range deriver.reportPaths() {
		entry := LastChangeEntry{
			Path: path, SourceID: uuid.Nil, RelationType: "", TargetID: uuid.Nil, Status: "",
			Change: nil, CandidateEventIDs: nil, LegacyEventIDs: nil,
		}
		if resolved, ok := resolveEntry(entry, deriver.fields[path], deriver.legacy, complete); ok {
			report.Fields = append(report.Fields, resolved)
		}
	}
	if deriver.path != "" {
		return report
	}
	for _, identity := range deriver.reportRelationships() {
		entry := LastChangeEntry{
			Path: "", SourceID: identity.sourceID, RelationType: identity.relationType, TargetID: identity.targetID,
			Status: "", Change: nil, CandidateEventIDs: nil, LegacyEventIDs: nil,
		}
		// A relationship identity without candidates comes from an
		// unsuccessful change row. Legacy rows do not identify relationships.
		// The identity reports partial when the rows read do not cover the
		// history, and the report leaves it out otherwise.
		candidates := deriver.relationships[identity]
		legacy := deriver.legacy
		if candidates == nil {
			legacy = nil
		}
		if resolved, ok := resolveEntry(entry, candidates, legacy, complete); ok {
			report.Relationships = append(report.Relationships, resolved)
		}
	}
	return report
}

// reportPaths returns the requested path, or every path that a change row
// names or that a record in a change row contains.
func (deriver *LastChangeDeriver) reportPaths() []string {
	if deriver.path != "" {
		return []string{deriver.path}
	}
	paths := make([]string, 0, len(deriver.recordPaths)+len(deriver.fields))
	for path := range deriver.recordPaths {
		paths = append(paths, path)
	}
	for path := range deriver.fields {
		if !deriver.recordPaths[path] {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

// reportRelationships returns every relationship identity that a row read
// names, including identities of unsuccessful changes.
func (deriver *LastChangeDeriver) reportRelationships() []relationshipIdentity {
	identities := make([]relationshipIdentity, 0, len(deriver.seenTargets))
	for identity := range deriver.seenTargets {
		identities = append(identities, identity)
	}
	slices.SortFunc(identities, func(left, right relationshipIdentity) int {
		return cmp.Or(
			cmp.Compare(left.sourceID.String(), right.sourceID.String()),
			cmp.Compare(left.relationType, right.relationType),
			cmp.Compare(left.targetID.String(), right.targetID.String()),
		)
	})
	return identities
}
