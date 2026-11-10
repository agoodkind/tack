package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type projectionManifestReader struct {
	decoder *json.Decoder
	last    *node.ProjectionManifestEntry
}

func newProjectionManifestReader(ctx context.Context, input io.Reader) (*projectionManifestReader, error) {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	token, err := decoder.Token()
	if err != nil {
		return nil, projectionFailure(ctx, "search.projection.decode_failed", fmt.Errorf("read manifest opening array: %w", err))
	}
	if token != json.Delim('[') {
		return nil, projectionFailure(ctx, "search.projection.invalid_manifest", fmt.Errorf("projection manifest must be a JSON array"))
	}
	return &projectionManifestReader{decoder: decoder, last: nil}, nil
}

func (r *projectionManifestReader) next(ctx context.Context) (*node.ProjectionManifestEntry, error) {
	if !r.decoder.More() {
		return nil, nil
	}
	var entry node.ProjectionManifestEntry
	if err := r.decoder.Decode(&entry); err != nil {
		return nil, projectionFailure(ctx, "search.projection.decode_failed", fmt.Errorf("decode projection manifest entry: %w", err))
	}
	if entry.OrgID == uuid.Nil || entry.PropertyDefID == uuid.Nil {
		return nil, projectionFailure(ctx, "search.projection.invalid_identity", fmt.Errorf("manifest entry has an empty organization or property definition ID"))
	}
	if err := entry.Search.Validate(ctx, slog.String("property_definition_id", entry.PropertyDefID.String())); err != nil {
		wrapped := fmt.Errorf("property definition %s: %w", entry.PropertyDefID, err)
		return nil, projectionFailure(ctx, "search.projection.invalid_rule", wrapped)
	}
	if r.last != nil {
		if entry.OrgID == r.last.OrgID && entry.PropertyDefID == r.last.PropertyDefID {
			return nil, projectionFailure(ctx, "search.projection.duplicate", fmt.Errorf("duplicate property definition %s", entry.PropertyDefID))
		}
		if !manifestEntryLess(*r.last, entry) {
			return nil, projectionFailure(ctx, "search.projection.unsorted", fmt.Errorf("projection manifest entries must be sorted by organization and property definition"))
		}
	}
	r.last = &entry
	return &entry, nil
}

func (r *projectionManifestReader) finish(ctx context.Context) error {
	token, err := r.decoder.Token()
	if err != nil {
		return projectionFailure(ctx, "search.projection.decode_failed", fmt.Errorf("read manifest closing array: %w", err))
	}
	if token != json.Delim(']') {
		return projectionFailure(ctx, "search.projection.invalid_manifest", fmt.Errorf("projection manifest lacks closing array"))
	}
	var trailing json.RawMessage
	if err := r.decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return projectionFailure(ctx, "search.projection.trailing_data", fmt.Errorf("projection manifest contains trailing JSON"))
		}
		return projectionFailure(ctx, "search.projection.decode_failed", fmt.Errorf("read manifest trailer: %w", err))
	}
	return nil
}

func manifestEntryLess(left, right node.ProjectionManifestEntry) bool {
	if left.OrgID != right.OrgID {
		return left.OrgID.String() < right.OrgID.String()
	}
	return left.PropertyDefID.String() < right.PropertyDefID.String()
}
