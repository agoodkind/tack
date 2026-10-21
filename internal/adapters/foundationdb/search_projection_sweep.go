package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// projectionSweepOrganizations limits how many organizations one sweep
// step reads.
const projectionSweepOrganizations = 10

// searchProjectionSweep records the environment's sweep of organizations
// for one writer's renderer and format. Cursor records the last
// organization ID the sweep read. Done reports whether the sweep completed.
type searchProjectionSweep struct {
	Renderer int    `json:"renderer"`
	Format   int    `json:"format"`
	Cursor   string `json:"cursor"`
	Done     bool   `json:"done"`
}

func searchProjectionSweepKey() []byte {
	return withPrefix(tuple.Tuple{keySearchProjectionSweep}.Pack())
}

// versionBefore reports whether renderer and format precede this writer's
// renderer and format in version order.
func versionBefore(renderer, format int) bool {
	if renderer != node.SearchRendererVersion {
		return renderer < node.SearchRendererVersion
	}
	return format < searchProjectionFormat
}

// sweepProjections runs one bounded sweep step for this writer. It requests
// activation of this writer's projection for each organization with an
// older projection state, including organizations without pending search
// work. If this writer's sweep already completed, it reads one key and
// does not write. A writer older than the recorded sweep does not run
// the sweep.
func (s *SearchWorkStore) sweepProjections(ctx context.Context) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.sweep_projections")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		sweep := searchProjectionSweep{Renderer: legacyRenderer, Format: legacyFormat, Cursor: "", Done: false}
		found, err := readSearchRecord(ctx, tr, searchProjectionSweepKey(), &sweep)
		if err != nil {
			return err
		}
		if !found || versionBefore(sweep.Renderer, sweep.Format) {
			sweep = searchProjectionSweep{Renderer: node.SearchRendererVersion, Format: searchProjectionFormat, Cursor: "", Done: false}
		}
		if sweep.Done || sweep.Renderer != node.SearchRendererVersion || sweep.Format != searchProjectionFormat {
			return nil
		}
		organizations, done, err := nextOrganizations(ctx, tr, sweep.Cursor)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		for _, orgID := range organizations {
			state, _, readErr := readProjectionState(ctx, tr, orgID)
			if readErr != nil {
				return readErr
			}
			if !state.olderThanWriter() {
				continue
			}
			if requestErr := requestProjectionActivation(ctx, tr, now, orgID); requestErr != nil {
				return requestErr
			}
		}
		if len(organizations) > 0 {
			sweep.Cursor = organizations[len(organizations)-1].String()
		}
		sweep.Done = done
		if done {
			telemetry.L(ctx).InfoContext(ctx, "search.projection.sweep_completed",
				slog.Int("renderer", sweep.Renderer), slog.Int("format", sweep.Format))
		}
		return writeSearchRecord(ctx, tr, searchProjectionSweepKey(), sweep)
	})
	return err
}

// nextOrganizations returns at most projectionSweepOrganizations IDs of
// organizations that store a node type, in key order after cursor.
// Each organization requires one range read of one key. It reports
// completion when a range read does not find another organization.
func nextOrganizations(ctx context.Context, tr fdb.Transaction, cursor string) ([]uuid.UUID, bool, error) {
	family, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keyNodeTypeDef}.Pack()))
	if err != nil {
		return nil, false, searchReadFailure(ctx, "create node type range", err)
	}
	begin := family.Begin.FDBKey()
	if cursor != "" {
		orgID, parseErr := uuid.Parse(cursor)
		if parseErr != nil {
			return nil, false, searchReadFailure(ctx, "parse projection sweep cursor", parseErr)
		}
		if begin, err = afterOrganization(ctx, orgID); err != nil {
			return nil, false, err
		}
	}
	organizations := make([]uuid.UUID, 0, projectionSweepOrganizations)
	for len(organizations) < projectionSweepOrganizations {
		items, readErr := tr.GetRange(fdb.KeyRange{Begin: begin, End: family.End}, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
		if readErr != nil {
			return nil, false, searchReadFailure(ctx, "read node type of next organization", readErr)
		}
		if len(items) == 0 {
			return organizations, true, nil
		}
		orgID, decodeErr := keyOrganization(ctx, items[0].Key)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		organizations = append(organizations, orgID)
		if begin, err = afterOrganization(ctx, orgID); err != nil {
			return nil, false, err
		}
	}
	return organizations, false, nil
}

// afterOrganization returns the first key after every node type key of
// orgID.
func afterOrganization(ctx context.Context, orgID uuid.UUID) (fdb.Key, error) {
	end, err := fdb.Strinc(nodeTypeDefPrefix(orgID))
	if err != nil {
		return nil, searchReadFailure(ctx, "compute node type range end of organization "+orgID.String(), err)
	}
	return fdb.Key(end), nil
}

// keyOrganization decodes the organization ID in the second tuple element
// of a node type key.
func keyOrganization(ctx context.Context, key fdb.Key) (uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "unpack node type key", err)
	}
	if len(values) < 2 {
		return uuid.Nil, searchReadFailure(ctx, "decode node type key", errors.New("node type key has no organization"))
	}
	text, isText := values[1].(string)
	orgID, parseErr := uuid.Parse(text)
	if !isText || parseErr != nil {
		return uuid.Nil, searchReadFailure(ctx, "decode node type key", errors.New("node type key organization is not a UUID"))
	}
	return orgID, nil
}
