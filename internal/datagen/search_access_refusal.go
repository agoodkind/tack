package datagen

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// searchAccessDeniedResponse is the tack_search problem text for a caller
	// that the authoritative search access check refuses.
	searchAccessDeniedResponse = "the caller cannot search this entry point"
	// permissionDeniedHeading starts the MCP refusal result when a handler
	// returns data without a membership check.
	permissionDeniedHeading = "#### Permission denied"
)

// IsSearchAuthorizationRefusal reports whether a tack_search response under
// entry matches a recognized authorization refusal.
//
// statusCode supplies the MCP request's HTTP status, or zero for an MCP result.
// toolErrorText supplies the text of an MCP isError result. The entry resolver
// searches only the caller's organizations and reports other entries as not
// found. The classifier accepts these responses:
//   - HTTP 401 and 403 indicate refusal.
//   - Tool text containing the entry's not-found message indicates refusal.
//   - Tool text containing searchAccessDeniedResponse indicates refusal.
//   - Tool text starting with permissionDeniedHeading indicates refusal.
//
// Other tool errors, including outage text and unexpected server errors, do
// not indicate refusal.
func IsSearchAuthorizationRefusal(statusCode int, toolErrorText, entry string) bool {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return true
	}
	if toolErrorText == "" {
		return false
	}
	entryNotFound := fmt.Sprintf("reference %q: not found", entry)
	if strings.Contains(toolErrorText, entryNotFound) {
		return true
	}
	if strings.Contains(toolErrorText, searchAccessDeniedResponse) {
		return true
	}
	return strings.HasPrefix(toolErrorText, permissionDeniedHeading)
}

// isAuthorizationRefusal applies [IsSearchAuthorizationRefusal] to an error
// from the datagen driver.
func isAuthorizationRefusal(err error, entry string) bool {
	var statusError *httpStatusError
	if errors.As(err, &statusError) {
		return IsSearchAuthorizationRefusal(statusError.statusCode, "", entry)
	}
	var toolError *toolCallError
	if errors.As(err, &toolError) {
		return IsSearchAuthorizationRefusal(0, toolError.message, entry)
	}
	return false
}
