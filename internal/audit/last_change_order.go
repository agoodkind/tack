package audit

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// changeCandidates stores the successful changes of one entry that can still
// be the latest one. A commit version orders rows only within the
// FoundationDB outbox, so versioned keeps the highest versioned change, and
// unversioned keeps every change without a commit version.
type changeCandidates struct {
	versioned   *LastChange
	unversioned []LastChange
}

// add keeps change when it can be the latest change of the entry.
func (candidates *changeCandidates) add(change LastChange) {
	if change.CommitVersion == "" {
		candidates.unversioned = append(candidates.unversioned, change)
		return
	}
	if candidates.versioned == nil || compareChanges(change, *candidates.versioned) > 0 {
		kept := change
		candidates.versioned = &kept
	}
}

// ambiguous reports whether two or more candidates exist and one of them lacks
// a commit version.
func (candidates *changeCandidates) ambiguous() bool {
	count := len(candidates.unversioned)
	if candidates.versioned != nil {
		count++
	}
	return count > 1 && len(candidates.unversioned) > 0
}

// winner returns the latest change when the candidates are not ambiguous.
func (candidates *changeCandidates) winner() LastChange {
	if candidates.versioned != nil {
		return *candidates.versioned
	}
	return candidates.unversioned[0]
}

// eventIDs returns the candidate event IDs in event time order, then event ID
// order.
func (candidates *changeCandidates) eventIDs() []uuid.UUID {
	changes := slices.Clone(candidates.unversioned)
	if candidates.versioned != nil {
		changes = append(changes, *candidates.versioned)
	}
	slices.SortFunc(changes, func(left, right LastChange) int {
		if order := left.EventTime.Compare(right.EventTime); order != 0 {
			return order
		}
		return cmp.Compare(left.EventID.String(), right.EventID.String())
	})
	identifiers := make([]uuid.UUID, 0, len(changes))
	for _, change := range changes {
		identifiers = append(identifiers, change.EventID)
	}
	return identifiers
}

// compareChanges orders two changes by commit version when both have one,
// then by event time, then by event ID.
func compareChanges(left, right LastChange) int {
	if left.CommitVersion != "" && right.CommitVersion != "" {
		if order := cmp.Compare(left.CommitVersion, right.CommitVersion); order != 0 {
			return order
		}
	}
	if order := left.EventTime.Compare(right.EventTime); order != 0 {
		return order
	}
	return cmp.Compare(left.EventID.String(), right.EventID.String())
}

// legacyEvidence is one legacy change row of the node. Its row cannot name the
// fields or the relationship it changed.
type legacyEvidence struct {
	change LastChange
}

// notOlderThan reports whether the legacy row can be newer than the latest
// known change of an entry. It compares commit versions when both rows have
// one and event times otherwise. An equal event time counts as not older.
func (legacy legacyEvidence) notOlderThan(known LastChange) bool {
	if legacy.change.CommitVersion != "" && known.CommitVersion != "" {
		return legacy.change.CommitVersion > known.CommitVersion
	}
	return !legacy.change.EventTime.Before(known.EventTime)
}

// readCoverage states which part of the node's history the rows read cover.
// complete is true when the rows read include the node's creation and the
// read was not truncated. truncated is true when the read stopped at its row
// limit before the oldest row of the window.
type readCoverage struct {
	complete  bool
	truncated bool
}

// resolveEntry sets the status, change, and evidence of entry from the
// candidates of the entry and the legacy rows of the node. It returns false
// when the entry has no evidence and the rows read cover the node's history.
//
// A truncated read reads rows by event time, and a row that it did not read
// can have an older event time and a later commit version. After a truncated
// read, an entry with candidates therefore reports partial: it keeps the
// observed latest candidate as Change when the candidates are ordered, and it
// lists the observed candidates.
func resolveEntry(entry LastChangeEntry, candidates *changeCandidates, legacy []legacyEvidence, coverage readCoverage) (LastChangeEntry, bool) {
	if candidates == nil {
		if len(legacy) > 0 {
			entry.Status = LastChangeUnknown
			entry.LegacyEventIDs = legacyEventIDs(legacy, nil)
			return entry, true
		}
		entry.Status = LastChangePartial
		return entry, !coverage.complete
	}
	if coverage.truncated {
		entry.Status = LastChangePartial
		entry.CandidateEventIDs = candidates.eventIDs()
		if !candidates.ambiguous() {
			winner := candidates.winner()
			entry.Change = &winner
		}
		return entry, true
	}
	if candidates.ambiguous() {
		entry.Status = LastChangeAmbiguousOrder
		entry.CandidateEventIDs = candidates.eventIDs()
		return entry, true
	}
	winner := candidates.winner()
	entry.Change = &winner
	newer := legacyEventIDs(legacy, &winner)
	if len(newer) > 0 {
		entry.Status = LastChangeUnknown
		entry.LegacyEventIDs = newer
		return entry, true
	}
	entry.Status = LastChangeKnown
	return entry, true
}

// legacyEventIDs returns the event IDs of the legacy rows that are not older
// than known, or of every legacy row when known is nil.
func legacyEventIDs(legacy []legacyEvidence, known *LastChange) []uuid.UUID {
	identifiers := []uuid.UUID{}
	for _, evidence := range legacy {
		if known == nil || evidence.notOlderThan(*known) {
			identifiers = append(identifiers, evidence.change.EventID)
		}
	}
	return identifiers
}
