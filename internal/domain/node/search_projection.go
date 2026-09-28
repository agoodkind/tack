package node

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// TextRule selects the searchable text that one property value produces.
// The content reader applies the rule to every property value of any node
// type.
type TextRule struct {
	Mode   string            `json:"mode"`
	Fields []TextField       `json:"fields,omitempty"`
	Items  *TextRule         `json:"items,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// TextField selects a field from an object value and applies a nested rule.
type TextField struct {
	Key  string   `json:"key"`
	Rule TextRule `json:"rule"`
}

// SearchProjection is the explicit searchable-text decision for a property.
type SearchProjection struct {
	Include bool     `json:"include"`
	Order   int      `json:"order"`
	Rule    TextRule `json:"rule"`
}

// ProjectionManifestEntry identifies one reviewed declaration in one org.
type ProjectionManifestEntry struct {
	OrgID         uuid.UUID        `json:"org_id"`
	PropertyDefID uuid.UUID        `json:"property_def_id"`
	Search        SearchProjection `json:"search"`
}

// ProjectionBackfillResult reports the definition counts of one manifest
// backfill and the identities it planned, applied, or found missing.
type ProjectionBackfillResult struct {
	Scanned    int                  `json:"scanned"`
	Changed    int                  `json:"changed"`
	Unchanged  int                  `json:"unchanged"`
	Missing    int                  `json:"missing"`
	PlannedIDs []ProjectionIdentity `json:"planned_ids,omitempty"`
	AppliedIDs []ProjectionIdentity `json:"applied_ids,omitempty"`
	MissingIDs []ProjectionIdentity `json:"missing_ids,omitempty"`
}

// ProjectionIdentity identifies one property declaration within one
// organization.
type ProjectionIdentity struct {
	OrgID         uuid.UUID `json:"org_id"`
	PropertyDefID uuid.UUID `json:"property_def_id"`
}

// LoggedProjectionError marks an error that request telemetry already logged.
type LoggedProjectionError struct{ Cause error }

// Error returns the original failure text.
func (e LoggedProjectionError) Error() string { return e.Cause.Error() }

// Unwrap returns the original failure.
func (e LoggedProjectionError) Unwrap() error { return e.Cause }

const (
	// TextRuleScalar extracts a scalar value.
	TextRuleScalar = "scalar"
	// TextRuleObject selects declared object fields.
	TextRuleObject = "object"
	// TextRuleItems applies a nested rule to list items.
	TextRuleItems = "items"
	// TextRuleLabels maps opaque values to declared labels.
	TextRuleLabels   = "labels"
	maxTextRuleDepth = 8
)

// Validate returns an error and logs a request-scoped event when the
// projection has a negative order or an invalid text rule. It reads only the
// projection and ignores the property type and secondary-index metadata.
func (p SearchProjection) Validate(ctx context.Context, fields ...slog.Attr) error {
	var err error
	if p.Order < 0 {
		err = fmt.Errorf("search projection order must not be negative")
	} else {
		err = p.Rule.validate(0)
	}
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("validate search projection: %w", err)
	attributes := make([]any, 0, len(fields)+1)
	attributes = append(attributes, slog.String("err", wrapped.Error()))
	for _, field := range fields {
		attributes = append(attributes, field)
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.projection.validation_failed", attributes...)
	return LoggedProjectionError{Cause: wrapped}
}

func (r TextRule) validate(depth int) error {
	if depth > maxTextRuleDepth {
		return fmt.Errorf("search projection rule exceeds recursion depth %d", maxTextRuleDepth)
	}
	switch r.Mode {
	case TextRuleScalar:
		return validateScalarRule(r)
	case TextRuleObject:
		if len(r.Fields) == 0 || r.Items != nil || len(r.Labels) != 0 {
			return fmt.Errorf("object search rule requires fields and no items or labels")
		}
		seen := make(map[string]struct{}, len(r.Fields))
		for _, field := range r.Fields {
			if strings.TrimSpace(field.Key) == "" {
				return fmt.Errorf("object search rule field key is empty")
			}
			if _, ok := seen[field.Key]; ok {
				return fmt.Errorf("object search rule field %q is duplicated", field.Key)
			}
			seen[field.Key] = struct{}{}
			if err := field.Rule.validate(depth + 1); err != nil {
				return fmt.Errorf("field %q: %s", field.Key, err.Error())
			}
		}
		return nil
	case TextRuleItems:
		return validateItemsRule(r, depth)
	case TextRuleLabels:
		return validateLabelsRule(r)
	default:
		return fmt.Errorf("unknown search rule mode %q", r.Mode)
	}
}

func validateScalarRule(rule TextRule) error {
	if len(rule.Fields) != 0 || rule.Items != nil || len(rule.Labels) != 0 {
		return fmt.Errorf("scalar search rule cannot include fields, items, or labels")
	}
	return nil
}

func validateItemsRule(rule TextRule, depth int) error {
	if rule.Items == nil || len(rule.Fields) != 0 || len(rule.Labels) != 0 {
		return fmt.Errorf("items search rule requires one nested item rule")
	}
	if err := rule.Items.validate(depth + 1); err != nil {
		return fmt.Errorf("items rule: %s", err.Error())
	}
	return nil
}

func validateLabelsRule(rule TextRule) error {
	if len(rule.Labels) == 0 || len(rule.Fields) != 0 || rule.Items != nil {
		return fmt.Errorf("labels search rule requires a label map")
	}
	for key, value := range rule.Labels {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("labels search rule contains an empty key or value")
		}
	}
	return nil
}
