package audit

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Report returns field entries in path order and relationship entries in
// source, relation type, and target order.
//
// The truncated argument reports whether the read stopped before the oldest row
// of the window. Coverage affects the returned entries as follows:
//
//   - Entries without candidates or legacy evidence report partial when
//     truncated is true or the rows do not include the node's creation.
//   - Every entry with candidates reports partial when truncated is true.
func (deriver *LastChangeDeriver) Report(truncated bool) LastChangeReport {
	coverage := readCoverage{complete: deriver.historyStart && !truncated, truncated: truncated}
	report := LastChangeReport{
		NodeID: deriver.nodeID, HistoryStartInWindow: deriver.historyStart,
		LegacyRowCount:             len(deriver.legacy) + len(deriver.legacyRelationships),
		LegacyRelationshipEventIDs: legacyEventIDs(deriver.legacyRelationships, nil),
		Fields:                     []LastChangeEntry{}, Relationships: []LastChangeEntry{},
	}
	for _, path := range deriver.reportPaths() {
		entry := LastChangeEntry{
			Path: path, SourceID: uuid.Nil, RelationType: "", TargetID: uuid.Nil, Status: "",
			Change: nil, CandidateEventIDs: nil, LegacyEventIDs: nil,
		}
		if resolved, ok := resolveEntry(entry, deriver.fields[path], deriver.legacy, coverage); ok {
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
		candidates := deriver.relationships[identity]
		if resolved, ok := resolveEntry(entry, candidates, deriver.relationshipLegacy(identity, candidates), coverage); ok {
			report.Relationships = append(report.Relationships, resolved)
		}
	}
	return report
}

// relationshipLegacy returns the legacy rows that apply to the relationship
// entry for identity. Legacy relationship rows record only the source node.
// They apply to entries with the node as source and do not apply to entries
// with the node as target. Legacy node rows do not apply to relationship
// entries.
//
// An identity without candidates comes from an unsuccessful change row. The
// report includes that identity as partial when the read does not cover the
// history and omits it otherwise. LastChangeReport.LegacyRelationshipEventIDs
// lists the legacy relationship rows that the identity does not show.
func (deriver *LastChangeDeriver) relationshipLegacy(identity relationshipIdentity, candidates *changeCandidates) []legacyEvidence {
	if candidates == nil || identity.sourceID != deriver.nodeID {
		return nil
	}
	return deriver.legacyRelationships
}

// reportPaths returns the requested path, or every path that a change row
// lists or that a record in a change row contains.
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

// reportRelationships returns every relationship identity in the rows,
// including identities from unsuccessful changes.
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
