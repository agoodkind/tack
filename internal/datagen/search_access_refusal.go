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
	// permissionDeniedHeading starts the fail-closed MCP result for a handler
	// that returned data without a membership check.
	permissionDeniedHeading = "#### Permission denied"
)

// IsSearchAuthorizationRefusal reports whether one tack_search response under
// entry is an authorization refusal. statusCode is the HTTP status of the MCP
// request, or 0 when the request returned an MCP result. toolErrorText is the
// text of an MCP isError result. HTTP 401 and 403 are refusals. The entry
// resolver searches only the caller's organizations and reports an entry
// outside them as not found. Every other tool error, including the outage
// text and an unexpected server error, is not a refusal.
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

// isAuthorizationRefusal applies IsSearchAuthorizationRefusal to an error
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
