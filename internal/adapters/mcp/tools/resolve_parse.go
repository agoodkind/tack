package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseNodeIdentifier splits a sequence reference into its project reference
// and positive sequence number.
//
// It splits at the last hyphen and rejects a missing project reference, a
// missing sequence, or a sequence that is not a positive integer.
//
// The documented example produces these values:
//
//   - "ENG-42" returns "ENG" and 42.
func ParseNodeIdentifier(identifier string) (projectReference string, seqID int, err error) {
	idx := strings.LastIndex(identifier, "-")
	if idx <= 0 || idx == len(identifier)-1 {
		return "", 0, fmt.Errorf("invalid identifier %q: expected PROJECT-N format (e.g. ENG-42)", identifier)
	}
	seq, convErr := strconv.Atoi(identifier[idx+1:])
	if convErr != nil || seq <= 0 {
		return "", 0, fmt.Errorf("invalid identifier %q: sequence must be a positive integer", identifier)
	}
	return identifier[:idx], seq, nil
}
