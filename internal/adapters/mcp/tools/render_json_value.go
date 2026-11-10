package tools

import (
	"bytes"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
)

// canonicalJSON returns equal comparison text for equal JSON values.
// It returns false when it cannot parse a value.
func canonicalJSON(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	switch trimmed[0] {
	case '{':
		return canonicalJSONObject(trimmed)
	case '[':
		return canonicalJSONArray(trimmed)
	case '"':
		return canonicalJSONString(trimmed)
	case 't', 'f', 'n':
		return string(trimmed), true
	default:
		number, ok := new(big.Rat).SetString(string(trimmed))
		if !ok {
			return "", false
		}
		return number.RatString(), true
	}
}

func canonicalJSONString(raw []byte) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func canonicalJSONObject(raw []byte) (string, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return "", false
	}
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", false
		}
		value, ok := canonicalJSON(members[key])
		if !ok {
			return "", false
		}
		parts = append(parts, string(encodedKey)+":"+value)
	}
	return "{" + strings.Join(parts, ",") + "}", true
}

func canonicalJSONArray(raw []byte) (string, bool) {
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return "", false
	}
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		value, ok := canonicalJSON(element)
		if !ok {
			return "", false
		}
		parts = append(parts, value)
	}
	return "[" + strings.Join(parts, ",") + "]", true
}

// sameJSON compares canonical values and compares the original bytes instead
// when either value cannot be canonicalized.
func sameJSON(left, right json.RawMessage) bool {
	leftCanonical, leftOK := canonicalJSON(left)
	rightCanonical, rightOK := canonicalJSON(right)
	if !leftOK || !rightOK {
		return bytes.Equal(left, right)
	}
	return leftCanonical == rightCanonical
}

func isAbsentJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// scalarOrCompactJSON returns scalar text or compact JSON for an object or array.
func scalarOrCompactJSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err == nil {
			return compact.String()
		}
	}
	return propAsString(trimmed)
}
