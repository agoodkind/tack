package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/google/uuid"
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
	// Diagnostics contains the complete diagnostic set for the revision.
	// Every page of the revision repeats this set.
	Diagnostics []PropertyDiagnostic `exhaustruct:"optional"`
}

// invalidValueError reports a value that its text rule cannot render. The
// message does not contain the value.
type invalidValueError struct{ message string }

func (e invalidValueError) Error() string { return e.message }

func extractProjectedLeaves(raw json.RawMessage, rule TextRule) ([]string, error) {
	var value json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, invalidValueError{message: "the value is not valid JSON"}
	}
	return extractRuleLeaves(value, rule)
}

func extractRuleLeaves(raw json.RawMessage, rule TextRule) ([]string, error) {
	switch rule.Mode {
	case TextRuleScalar:
		return scalarLeaf(raw)
	case TextRuleItems:
		return itemLeaves(raw, rule)
	case TextRuleObject:
		return objectLeaves(raw, rule)
	case TextRuleLabels:
		return labelLeaf(raw, rule)
	default:
		return nil, invalidValueError{message: "the text rule uses an unknown mode"}
	}
}

func scalarLeaf(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, invalidValueError{message: "the scalar value is empty"}
	}
	switch trimmed[0] {
	case '"':
		var scalar string
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return nil, invalidValueError{message: "the value is not a string"}
		}
		return []string{scalar}, nil
	case 't', 'f':
		var scalar bool
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return nil, invalidValueError{message: "the value is not a boolean"}
		}
		return []string{strconv.FormatBool(scalar)}, nil
	default:
		var scalar json.Number
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return nil, invalidValueError{message: "the value is not a scalar"}
		}
		return []string{scalar.String()}, nil
	}
}

func itemLeaves(raw json.RawMessage, rule TextRule) ([]string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, invalidValueError{message: "the value is not a list"}
	}
	var leaves []string
	for _, item := range items {
		itemLeaves, err := extractRuleLeaves(item, *rule.Items)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, itemLeaves...)
	}
	return leaves, nil
}

func objectLeaves(raw json.RawMessage, rule TextRule) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, invalidValueError{message: "the value is not an object"}
	}
	declared := make(map[string]TextRule, len(rule.Fields))
	for _, field := range rule.Fields {
		declared[field.Key] = field.Rule
	}
	for key := range fields {
		if _, exists := declared[key]; !exists {
			return nil, invalidValueError{message: "the object contains a field outside the text rule's declaration"}
		}
	}
	orderedKeys := make([]string, 0, len(fields))
	for key := range fields {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	var leaves []string
	for _, key := range orderedKeys {
		fieldLeaves, err := extractRuleLeaves(fields[key], declared[key])
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
		return nil, invalidValueError{message: "the label value is not a string"}
	}
	value, exists := rule.Labels[label]
	if !exists {
		return nil, invalidValueError{message: "the text rule does not declare a label for the value"}
	}
	return []string{value}, nil
}
