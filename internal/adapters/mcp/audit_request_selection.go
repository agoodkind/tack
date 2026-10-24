package mcp

import (
	"bytes"
	"encoding/json"
	"strings"

	"goodkind.io/tack/internal/audit"
)

const (
	// maxRequestPathBytes limits the byte length of request paths in read events.
	maxRequestPathBytes = 256
	// maxHTTPMethodBytes limits the byte length of HTTP methods in read events.
	maxHTTPMethodBytes = 16
	// requestPathPunctuation lists the characters other than letters and
	// digits that a recorded request path can contain.
	requestPathPunctuation = "/-_.~"
)

// readSelection returns the normalized arguments of a read. The node_id
// string becomes Reference. Every other argument with a checked name becomes
// a compact JSON value in Criteria. It returns nil when arguments do not
// decode as a JSON object or do not contain a checked argument.
func readSelection(arguments json.RawMessage) *audit.ReadSelection {
	values := map[string]json.RawMessage{}
	if len(arguments) == 0 || json.Unmarshal(arguments, &values) != nil {
		return nil
	}
	selection := &audit.ReadSelection{Reference: "", Criteria: map[string]json.RawMessage{}}
	for name, value := range values {
		if checkedName(name) == "" {
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, value) != nil {
			continue
		}
		if name == nodeIDArgument {
			var reference string
			if json.Unmarshal(compact.Bytes(), &reference) == nil {
				selection.Reference = strings.TrimSpace(reference)
				continue
			}
		}
		selection.Criteria[name] = json.RawMessage(compact.Bytes())
	}
	if len(selection.Criteria) == 0 {
		selection.Criteria = nil
	}
	if selection.Reference == "" && selection.Criteria == nil {
		return nil
	}
	return selection
}

// checkedMethod returns method when it contains only uppercase letters
// within maxHTTPMethodBytes. It returns an empty string otherwise.
func checkedMethod(method string) string {
	if method == "" || len(method) > maxHTTPMethodBytes {
		return ""
	}
	for _, character := range method {
		if character < 'A' || character > 'Z' {
			return ""
		}
	}
	return method
}

// checkedPath returns path when it starts with a slash and contains only
// letters, digits, and requestPathPunctuation within maxRequestPathBytes. It
// returns an empty string otherwise.
func checkedPath(path string) string {
	if !strings.HasPrefix(path, "/") || len(path) > maxRequestPathBytes {
		return ""
	}
	for _, character := range path {
		letter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		digit := character >= '0' && character <= '9'
		if !letter && !digit && !strings.ContainsRune(requestPathPunctuation, character) {
			return ""
		}
	}
	return path
}
