package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// ErrContentChanged reports that a continuation no longer matches its source revision.
var ErrContentChanged = errors.New("search content changed")

// SearchAccess contains opaque values used to filter indexed pages.
type SearchAccess struct {
	Versions   []string `json:"versions"`
	Keys       []string `json:"keys"`
	Generation int64    `json:"generation"`
}

// ContentPage is one bounded page of searchable text from a committed revision.
type ContentPage struct {
	NodeID            uuid.UUID
	NodeType          string
	Revision          string
	ProjectionVersion string
	Name              string
	Text              string
	Access            SearchAccess
	Ordinal           uint64
	OverlapBytes      int
	NextCursor        string
	Done              bool
}

func extractProjectedLeaves(ctx context.Context, raw json.RawMessage, rule TextRule) ([]string, error) {
	var value json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		wrapped := fmt.Errorf("decode projected value: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.value_decode_failed", slog.String("err", wrapped.Error()))
		return nil, LoggedProjectionError{Cause: wrapped}
	}
	return extractRuleLeaves(ctx, value, rule)
}

func extractRuleLeaves(ctx context.Context, raw json.RawMessage, rule TextRule) ([]string, error) {
	switch rule.Mode {
	case TextRuleScalar:
		return scalarLeaf(ctx, raw)
	case TextRuleItems:
		return itemLeaves(ctx, raw, rule)
	case TextRuleObject:
		return objectLeaves(ctx, raw, rule)
	case TextRuleLabels:
		return labelLeaf(raw, rule)
	default:
		return nil, ErrInvalidSearchProjection
	}
}

func scalarLeaf(ctx context.Context, raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, ErrInvalidSearchProjection
	}
	switch trimmed[0] {
	case '"':
		var scalar string
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return projectionValueDecodeFailure(ctx, "decode string scalar", err)
		}
		return []string{scalar}, nil
	case 't', 'f':
		var scalar bool
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return projectionValueDecodeFailure(ctx, "decode boolean scalar", err)
		}
		return []string{strconv.FormatBool(scalar)}, nil
	default:
		var scalar json.Number
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return projectionValueDecodeFailure(ctx, "decode numeric scalar", err)
		}
		return []string{scalar.String()}, nil
	}
}

func projectionValueDecodeFailure(ctx context.Context, operation string, err error) ([]string, error) {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.projection.scalar_decode_failed", slog.String("err", wrapped.Error()))
	return nil, LoggedProjectionError{Cause: wrapped}
}

func itemLeaves(ctx context.Context, raw json.RawMessage, rule TextRule) ([]string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		wrapped := fmt.Errorf("decode projected items: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.items_decode_failed", slog.String("err", wrapped.Error()))
		return nil, LoggedProjectionError{Cause: wrapped}
	}
	if items == nil {
		return nil, ErrInvalidSearchProjection
	}
	var leaves []string
	for _, item := range items {
		itemLeaves, err := extractRuleLeaves(ctx, item, *rule.Items)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, itemLeaves...)
	}
	return leaves, nil
}

// IsLoggedProjectionError reports whether err wraps a
// [LoggedProjectionError], which request telemetry already logged.
func IsLoggedProjectionError(err error) bool {
	var logged LoggedProjectionError
	return errors.As(err, &logged)
}

func objectLeaves(ctx context.Context, raw json.RawMessage, rule TextRule) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		wrapped := fmt.Errorf("decode projected object: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.object_decode_failed", slog.String("err", wrapped.Error()))
		return nil, LoggedProjectionError{Cause: wrapped}
	}
	if fields == nil {
		return nil, ErrInvalidSearchProjection
	}
	declared := make(map[string]TextRule, len(rule.Fields))
	for _, field := range rule.Fields {
		declared[field.Key] = field.Rule
	}
	for key := range fields {
		if _, exists := declared[key]; !exists {
			return nil, ErrInvalidSearchProjection
		}
	}
	orderedKeys := make([]string, 0, len(fields))
	for key := range fields {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	var leaves []string
	for _, key := range orderedKeys {
		fieldLeaves, err := extractRuleLeaves(ctx, fields[key], declared[key])
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, fieldLeaves...)
	}
	return leaves, nil
}

func labelLeaf(raw json.RawMessage, rule TextRule) ([]string, error) {
	var label string
	if err := json.Unmarshal(raw, &label); err != nil {
		return nil, ErrInvalidSearchProjection
	}
	value, exists := rule.Labels[label]
	if !exists {
		return nil, ErrInvalidSearchProjection
	}
	return []string{value}, nil
}
