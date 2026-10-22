package foundationdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// legacyRenderer is the renderer version for an organization without a
// projection state record. Earlier releases wrote the organization's pages
// without recording a renderer version.
const legacyRenderer = 0

// legacyFormat is the projection format of an organization without a
// projection state record. Its digest is the sum at searchProjectionKey.
const legacyFormat = 1

// searchProjectionState records one organization's active projection.
// Renderer identifies the text its pages use. Digest stores the hex sum
// of every property definition's and node type's contributions under
// Format. Activation records the rescan generation that activated the
// state. Each activation produces a new projection identity.
type searchProjectionState struct {
	Renderer   int    `json:"renderer"`
	Format     int    `json:"format"`
	Digest     string `json:"digest"`
	Activation int64  `json:"activation"`
}

type projectionIdentityInput struct {
	Pagination int    `json:"pagination"`
	Renderer   int    `json:"renderer"`
	Format     int    `json:"format"`
	Digest     string `json:"digest"`
	Activation int64  `json:"activation"`
}

func searchProjectionStateKey(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchProjectionState, orgID.String()}.Pack())
}

// readProjectionState reads the organization's projection state.
// It returns the legacy renderer and format when the record does not exist.
func readProjectionState(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (searchProjectionState, bool, error) {
	state := searchProjectionState{Renderer: legacyRenderer, Format: legacyFormat, Digest: "", Activation: 0}
	found, err := readSearchRecord(ctx, tr, searchProjectionStateKey(orgID), &state)
	return state, found, err
}

// writerProjection reports whether state uses this writer's renderer and
// format.
func (state searchProjectionState) writerProjection() bool {
	return state.Renderer == node.SearchRendererVersion && state.Format == searchProjectionFormat
}

// olderThanWriter reports whether state's renderer and format precede
// this writer's renderer and format in version order.
func (state searchProjectionState) olderThanWriter() bool {
	return versionBefore(state.Renderer, state.Format)
}

// identity returns the SHA-256 hash of the pagination version, Renderer,
// Format, Digest, and Activation. Content cursors, page document IDs, and
// checkpoints record this projection identity.
func (state searchProjectionState) identity(ctx context.Context) (string, error) {
	encoded, err := json.Marshal(projectionIdentityInput{
		Pagination: searchCursorVersion, Renderer: state.Renderer, Format: state.Format,
		Digest: state.Digest, Activation: state.Activation,
	})
	if err != nil {
		return "", searchReadFailure(ctx, "encode projection identity", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// projectionVersion returns the identity of the organization's active
// projection with one point read. It returns an error that wraps
// [node.ErrContentChanged] when this writer does not use the active
// renderer and format.
func projectionVersion(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (string, error) {
	state, _, err := readProjectionState(ctx, tr, orgID)
	if err != nil {
		return "", err
	}
	if !state.writerProjection() {
		telemetry.L(ctx).InfoContext(ctx, "search.projection.inactive", slog.String("org_id", orgID.String()),
			slog.Int("renderer", state.Renderer), slog.Int("format", state.Format))
		return "", loggedSearchError{err: inactiveProjectionError{orgID: orgID, state: state}}
	}
	return state.identity(ctx)
}

// inactiveProjectionError reports an organization's active projection
// that this writer does not produce. It matches [node.ErrContentChanged].
type inactiveProjectionError struct {
	orgID uuid.UUID
	state searchProjectionState
}

func (e inactiveProjectionError) Error() string {
	return fmt.Sprintf("organization %s uses search renderer %d format %d, and this writer uses renderer %d format %d: %s",
		e.orgID, e.state.Renderer, e.state.Format, node.SearchRendererVersion, searchProjectionFormat, node.ErrContentChanged.Error())
}

// Unwrap returns [node.ErrContentChanged].
func (e inactiveProjectionError) Unwrap() error { return node.ErrContentChanged }

// requireActiveProjection returns [node.ErrContentChanged] unless projection
// is the identity of the organization's active projection.
func requireActiveProjection(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, projection string) error {
	active, err := projectionVersion(ctx, tr, orgID)
	if err != nil {
		return err
	}
	if active != projection {
		return node.ErrContentChanged
	}
	return nil
}

// projectionChange records the changes that one metadata write makes to
// the projection digests. previous and next store the format contributions.
// legacyPrevious and legacyNext store the legacy declaration hashes.
// created reports whether the record did not exist before the write.
type projectionChange struct {
	previous       []byte
	next           []byte
	legacyPrevious []byte
	legacyNext     []byte
	created        bool
}

// applyProjectionChange updates the organization's projection for one
// metadata write. It updates the digest when the state uses this writer's
// renderer and format. It clears a state with another renderer or format.
// The next activation recomputes that digest. If the state does not exist,
// it updates the legacy digest. The first metadata record also creates
// the state for this writer.
func applyProjectionChange(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, change projectionChange) error {
	state, found, err := readProjectionState(ctx, tr, orgID)
	if err != nil {
		return err
	}
	switch {
	case found && state.writerProjection():
		state.Digest, err = replaceContribution(ctx, state.Digest, change.previous, change.next)
		if err != nil {
			return err
		}
		return writeSearchRecord(ctx, tr, searchProjectionStateKey(orgID), state)
	case found:
		tr.Clear(fdb.Key(searchProjectionStateKey(orgID)))
		return nil
	}
	if err := updateProjectionDigest(ctx, tr, orgID, change.legacyPrevious, change.legacyNext); err != nil {
		return err
	}
	if !change.created {
		return nil
	}
	empty, err := organizationHasNoMetadata(ctx, tr, orgID)
	if err != nil || !empty {
		return err
	}
	return writeSearchRecord(ctx, tr, searchProjectionStateKey(orgID), searchProjectionState{
		Renderer: node.SearchRendererVersion, Format: searchProjectionFormat,
		Digest: hex.EncodeToString(sumContributions(nil, nil, change.next)), Activation: 0,
	})
}

// organizationHasNoMetadata reports whether the organization does not store
// a property definition or a node type. It reads at most one key from each
// key family.
func organizationHasNoMetadata(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (bool, error) {
	for _, prefix := range [][]byte{propertyDefPrefix(orgID), nodeTypeDefPrefix(orgID)} {
		keyRange, err := fdb.PrefixRange(prefix)
		if err != nil {
			return false, searchReadFailure(ctx, "create metadata range", err)
		}
		items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
		if err != nil {
			return false, searchReadFailure(ctx, "read metadata of organization "+orgID.String(), err)
		}
		if len(items) > 0 {
			return false, nil
		}
	}
	return true, nil
}
