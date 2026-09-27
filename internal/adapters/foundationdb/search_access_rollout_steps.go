package foundationdb

import (
	"context"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// CompleteScan schedules access work for one bounded page of the
// authority's nodes and checkpoints the backfill or retirement scan in the
// same transaction. The final page starts verification.
func (s *SearchAccessRolloutStore) CompleteScan(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, scan searchdomain.ScanResult) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.complete_scan")(&err)
	err = s.rolloutStep(ctx, work, read, func(tr fdb.Transaction, record *searchRolloutRecord, _ searchWorkRecord) error {
		now := s.work.clock.Now()
		for _, nodeID := range scan.Nodes {
			if scheduleErr := scheduleExistingSearchChange(ctx, tr, now, work.OrgID, nodeID, searchChangeAccess); scheduleErr != nil {
				return scheduleErr
			}
		}
		retiring := record.Phase == string(searchdomain.AccessRetiring)
		switch {
		case !scan.Done && retiring:
			record.RetireCursor = scan.NextCursor
		case !scan.Done:
			record.ScanCursor = scan.NextCursor
		case retiring:
			record.ScanComplete, record.VerifyCursor = true, ""
		default:
			record.Phase, record.VerifyCursor = string(searchdomain.AccessVerifying), ""
		}
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.rollout.scan_failed", "checkpoint access rollout scan of authority "+work.OrgID.String(), uuid.Nil, err)
	}
	return nil
}

// BeginRetire starts removing the previous version after activation. The
// same transaction rereads the previous version's session presence. A
// session that opened under that version after the worker's check blocks
// retirement.
func (s *SearchAccessRolloutStore) BeginRetire(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.begin_retire")(&err)
	err = s.rolloutStep(ctx, work, read, func(tr fdb.Transaction, record *searchRolloutRecord, _ searchWorkRecord) error {
		if record.Phase != string(searchdomain.AccessVerifying) || record.ActiveVersion != record.CandidateVersion {
			return searchdomain.ErrWorkChanged
		}
		active, sessionErr := sessionVersionPresent(ctx, tr, s.work.clock.Now(), record.AuthorityID, record.PreviousVersion)
		if sessionErr != nil {
			return sessionErr
		}
		if active {
			return searchdomain.ErrWorkChanged
		}
		event, eventErr := readPermissionEvent(ctx, tr, record.AuthorityID)
		if eventErr != nil {
			return eventErr
		}
		record.Phase, record.WriteVersions = string(searchdomain.AccessRetiring), []string{record.ActiveVersion}
		record.RetireCursor, record.VerifyCursor, record.ScanComplete = "", "", false
		record.PermissionEventVersion = event
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.rollout.retire_failed", "begin access retirement of authority "+work.OrgID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rollout.retiring", slog.String("authority_id", work.OrgID.String()),
		slog.String("previous_version", read.PreviousVersion))
	return nil
}

// rolloutStep verifies the rollout claim and the rollout step the worker
// read, applies step to the stored record, and writes it, all in one
// transaction. step receives the verified rollout work record.
func (s *SearchAccessRolloutStore) rolloutStep(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, step func(fdb.Transaction, *searchRolloutRecord, searchWorkRecord) error) error {
	return transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		workRecord, err := s.work.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		record, err := readRollout(ctx, tr, work.OrgID)
		if err != nil {
			return err
		}
		if !record.matches(read) {
			return searchdomain.ErrWorkChanged
		}
		if err := step(tr, &record, workRecord); err != nil {
			return err
		}
		return writeSearchRecord(ctx, tr, searchRolloutKey(work.OrgID), record)
	})
}

// sessionVersionPresent reads, inside tr, whether a session of authorityID
// can still read OpenSearch under version.
func sessionVersionPresent(ctx context.Context, tr fdb.Transaction, now time.Time, authorityID uuid.UUID, version string) (bool, error) {
	prefix, err := fdb.PrefixRange(searchSessionVersionPrefix(authorityID, version))
	if err != nil {
		return false, searchReadFailure(ctx, "create session version range", err)
	}
	begin := fdb.Key(searchSessionVersionStart(authorityID, version, now))
	keys, err := tr.GetRange(fdb.KeyRange{Begin: begin, End: prefix.End}, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return false, searchReadFailure(ctx, "read session version presence", err)
	}
	return len(keys) > 0, nil
}
