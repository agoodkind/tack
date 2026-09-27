package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchRebuildRecord is the stored index replacement.
type searchRebuildRecord struct {
	ID            uuid.UUID `json:"id"`
	Mode          string    `json:"mode"`
	State         string    `json:"state"`
	SourceIndex   string    `json:"source_index"`
	TargetIndex   string    `json:"target_index"`
	ScanCursor    string    `json:"scan_cursor,omitempty"`
	VerifyCursor  string    `json:"verify_cursor,omitempty"`
	Failure       string    `json:"failure,omitempty"`
	PrimaryShards int       `json:"primary_shards"`
	RoutingShards int       `json:"routing_shards"`
	Replicas      int       `json:"replicas"`
	ScanComplete  bool      `json:"scan_complete"`
	Paused        bool      `json:"paused"`
	Restored      bool      `json:"restored"`
	PausedAt      time.Time `json:"paused_at"`
	Generation    int64     `json:"generation"`
}

func rebuildRecordFor(rebuild searchdomain.Rebuild) searchRebuildRecord {
	return searchRebuildRecord{
		ID: rebuild.ID, Mode: string(rebuild.Mode), State: string(rebuild.State),
		SourceIndex: rebuild.SourceIndex, TargetIndex: rebuild.TargetIndex,
		ScanCursor: rebuild.ScanCursor, VerifyCursor: rebuild.VerifyCursor, Failure: rebuild.Failure,
		PrimaryShards: rebuild.PrimaryShards, RoutingShards: rebuild.RoutingShards, Replicas: rebuild.Replicas,
		ScanComplete: rebuild.ScanComplete, Paused: rebuild.Paused, Restored: rebuild.Restored,
		PausedAt: rebuild.PausedAt, Generation: rebuild.Generation,
	}
}

func (r searchRebuildRecord) rebuild() searchdomain.Rebuild {
	return searchdomain.Rebuild{
		ID: r.ID, Mode: searchdomain.ReplacementMode(r.Mode), State: searchdomain.RebuildState(r.State),
		SourceIndex: r.SourceIndex, TargetIndex: r.TargetIndex,
		ScanCursor: r.ScanCursor, VerifyCursor: r.VerifyCursor, Failure: r.Failure,
		PrimaryShards: r.PrimaryShards, RoutingShards: r.RoutingShards, Replicas: r.Replicas,
		ScanComplete: r.ScanComplete, Paused: r.Paused, Restored: r.Restored,
		PausedAt: r.PausedAt, Generation: r.Generation,
	}
}

// readRebuild returns the one replacement record and whether it exists.
func readRebuild(ctx context.Context, tr fdb.Transaction) (searchdomain.Rebuild, bool, error) {
	var record searchRebuildRecord
	found, err := readSearchRecord(ctx, tr, searchRebuildKey(), &record)
	if err != nil || !found {
		return searchdomain.Rebuild{}, found, err
	}
	return record.rebuild(), true, nil
}

// claimScope is the serving index and replacement state one claim or
// checkpoint transaction read.
type claimScope struct {
	serving    string
	rebuild    searchdomain.Rebuild
	rebuilding bool
}

// readClaimScope reads the serving index and the replacement state in tr. It
// returns [searchdomain.ErrNoServingIndex] when no serving index is recorded.
func readClaimScope(ctx context.Context, tr fdb.Transaction) (claimScope, error) {
	serving, err := tr.Get(fdb.Key(searchIndexKey())).Get()
	if err != nil {
		return claimScope{}, searchReadFailure(ctx, "read serving search index", err)
	}
	if len(serving) == 0 {
		return claimScope{}, searchdomain.ErrNoServingIndex
	}
	rebuild, rebuilding, err := readRebuild(ctx, tr)
	if err != nil {
		return claimScope{}, err
	}
	return claimScope{serving: string(serving), rebuild: rebuild, rebuilding: rebuilding}, nil
}

// targets returns the index a claim of class writes and the replacement
// index it also writes. A copy item writes only the replacement target and
// is claimable only while the replacement accepts copies.
func (s claimScope) targets(class searchdomain.WorkClass) (string, string, bool) {
	if class == searchdomain.WorkClassCopy {
		accepting := s.rebuilding &&
			(s.rebuild.State == searchdomain.RebuildCopying || s.rebuild.State == searchdomain.RebuildVerifying)
		if !accepting {
			return "", "", false
		}
		return s.rebuild.TargetIndex, "", true
	}
	if class == searchdomain.WorkClassRebuild || !s.rebuilding {
		return s.serving, "", true
	}
	return s.serving, s.rebuild.Mirror(), true
}
