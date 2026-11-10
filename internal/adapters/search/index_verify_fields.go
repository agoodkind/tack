package search

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// compareAccessField returns one error for each strict access value that
// differs.
func compareAccessField(actualJSON, expectedJSON json.RawMessage) []error {
	var actual, expected accessDef
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		return []error{fmt.Errorf("decode access mapping: %w", err)}
	}
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		return []error{fmt.Errorf("decode expected access mapping: %w", err)}
	}
	var mismatches []error
	if actual.Dynamic != expected.Dynamic {
		mismatches = append(mismatches, fmt.Errorf("access dynamic setting is %q, want %q", actual.Dynamic, expected.Dynamic))
	}
	for _, key := range slices.Sorted(maps.Keys(expected.Fields)) {
		if actual.Fields[key] != expected.Fields[key] {
			mismatches = append(mismatches, fmt.Errorf("access field %s has type %q, want %q", key, actual.Fields[key].Type, expected.Fields[key].Type))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(actual.Fields)) {
		if _, declared := expected.Fields[key]; !declared {
			mismatches = append(mismatches, fmt.Errorf("access field %s is not declared", key))
		}
	}
	return mismatches
}

// compareSemanticField returns one error for each native semantic setting
// that differs.
func compareSemanticField(actualJSON, expectedJSON json.RawMessage) []error {
	var actual, expected semanticDef
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		return []error{fmt.Errorf("decode semantic mapping: %w", err)}
	}
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		return []error{fmt.Errorf("decode expected semantic mapping: %w", err)}
	}
	var mismatches []error
	for _, value := range []struct{ name, actual, expected string }{
		{name: "type", actual: actual.Type, expected: expected.Type},
		{name: "raw field type", actual: actual.RawFieldType, expected: expected.RawFieldType},
		{name: "model ID", actual: actual.ModelID, expected: expected.ModelID},
		{name: "semantic info field", actual: actual.SemanticInfoFieldName, expected: expected.SemanticInfoFieldName},
	} {
		if value.actual != value.expected {
			mismatches = append(mismatches, fmt.Errorf("semantic %s is %q, want %q", value.name, value.actual, value.expected))
		}
	}
	if actual.SparseEncodingConfig != expected.SparseEncodingConfig {
		mismatches = append(mismatches, fmt.Errorf("semantic sparse encoding is %+v, want %+v", actual.SparseEncodingConfig, expected.SparseEncodingConfig))
	}
	if actual.SkipExistingEmbedding != expected.SkipExistingEmbedding {
		mismatches = append(mismatches, fmt.Errorf("semantic skip_existing_embedding is %t, want %t", actual.SkipExistingEmbedding, expected.SkipExistingEmbedding))
	}
	if !slices.Equal(actual.Chunking, expected.Chunking) {
		mismatches = append(mismatches, fmt.Errorf("semantic chunking is %+v, want %+v", actual.Chunking, expected.Chunking))
	}
	return mismatches
}

// compareScalarField returns an error when one scalar property type differs.
func compareScalarField(actualJSON, expectedJSON json.RawMessage) []error {
	var actual, expected fieldDef
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		return []error{fmt.Errorf("decode field mapping: %w", err)}
	}
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		return []error{fmt.Errorf("decode expected field mapping: %w", err)}
	}
	if actual != expected {
		return []error{fmt.Errorf("field type is %q, want %q", actual.Type, expected.Type)}
	}
	return nil
}
