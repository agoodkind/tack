package foundationdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// searchRolloutRecord is the stored access policy rollout of one authority.
type searchRolloutRecord struct {
	AuthorityID            uuid.UUID `json:"authority_id"`
	ActiveVersion          string    `json:"active_version"`
	CandidateVersion       string    `json:"candidate_version,omitempty"`
	PreviousVersion        string    `json:"previous_version,omitempty"`
	WriteVersions          []string  `json:"write_versions"`
	Phase                  string    `json:"phase"`
	ScanCursor             string    `json:"scan_cursor,omitempty"`
	VerifyCursor           string    `json:"verify_cursor,omitempty"`
	RetireCursor           string    `json:"retire_cursor,omitempty"`
	ScanComplete           bool      `json:"scan_complete"`
	Generation             int64     `json:"generation"`
	PermissionEventVersion int64     `json:"permission_event_version"`
}

// stableRollout returns the state of an authority that never ran a rollout.
func stableRollout(authorityID uuid.UUID) searchRolloutRecord {
	return searchRolloutRecord{
		AuthorityID: authorityID, ActiveVersion: searchaccess.StableVersion, CandidateVersion: "",
		PreviousVersion: "", WriteVersions: []string{searchaccess.StableVersion},
		Phase: string(searchdomain.AccessStable), ScanCursor: "", VerifyCursor: "", RetireCursor: "",
		ScanComplete: false, Generation: 0, PermissionEventVersion: 0,
	}
}

// readRollout reads the rollout of authorityID. For an authority without a
// record, it returns stableRollout.
func readRollout(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID) (searchRolloutRecord, error) {
	var record searchRolloutRecord
	found, err := readSearchRecord(ctx, tr, searchRolloutKey(authorityID), &record)
	if err != nil {
		return record, err
	}
	if !found {
		return stableRollout(authorityID), nil
	}
	return record, nil
}

func (r searchRolloutRecord) rollout() searchdomain.AccessRollout {
	return searchdomain.AccessRollout{
		AuthorityID: r.AuthorityID, ActiveVersion: r.ActiveVersion, CandidateVersion: r.CandidateVersion,
		PreviousVersion: r.PreviousVersion, WriteVersions: slices.Clone(r.WriteVersions),
		Phase: searchdomain.AccessPhase(r.Phase), ScanCursor: r.ScanCursor, VerifyCursor: r.VerifyCursor,
		RetireCursor: r.RetireCursor, ScanComplete: r.ScanComplete, Generation: r.Generation,
		PermissionEventVersion: r.PermissionEventVersion,
	}
}

// matches reports whether the stored step equals the step the worker read.
// A mismatch means another claim or operator changed the rollout.
func (r searchRolloutRecord) matches(read searchdomain.AccessRollout) bool {
	return r.Generation == read.Generation && r.Phase == string(read.Phase) && r.ScanCursor == read.ScanCursor &&
		r.VerifyCursor == read.VerifyCursor && r.RetireCursor == read.RetireCursor &&
		r.ScanComplete == read.ScanComplete && r.ActiveVersion == read.ActiveVersion
}

// requireWrittenVersion refuses a new session under a version the
// authority no longer writes. The read conflicts with a concurrent
// retirement, and one of the two transactions retries.
func requireWrittenVersion(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, version string) error {
	record, err := readRollout(ctx, tr, authorityID)
	if err != nil {
		return sessionStepError{operation: "read access rollout of authority " + authorityID.String(), err: err}
	}
	if !slices.Contains(record.WriteVersions, version) {
		return sessionStepError{operation: "open session under access version " + version, err: searchdomain.ErrSessionChanged}
	}
	return nil
}

// addPermissionEvent increments the authority's permission event counter
// with an atomic add. The add reads no key and adds no read conflict.
func addPermissionEvent(tr fdb.Transaction, authorityID uuid.UUID) {
	var one [8]byte
	binary.LittleEndian.PutUint64(one[:], 1)
	tr.Add(fdb.Key(searchPermissionEventKey(authorityID)), one[:])
}

// readPermissionEvent reads the authority's permission event counter. An
// absent counter is zero.
func readPermissionEvent(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID) (int64, error) {
	encoded, err := tr.Get(fdb.Key(searchPermissionEventKey(authorityID))).Get()
	if err != nil {
		return 0, searchReadFailure(ctx, "read permission events of authority "+authorityID.String(), err)
	}
	if len(encoded) == 0 {
		return 0, nil
	}
	if len(encoded) != 8 {
		return 0, searchReadFailure(ctx, "decode permission events", fmt.Errorf("counter has %d bytes", len(encoded)))
	}
	value := binary.LittleEndian.Uint64(encoded)
	if value > math.MaxInt64 {
		return 0, searchReadFailure(ctx, "decode permission events", fmt.Errorf("counter %d overflows", value))
	}
	return int64(value), nil
}
