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

// isAuthorizationRefusal reports whether err is one of the authorization
// responses of tack_search under entry. The entry resolver searches only the
// caller's organizations and reports an entry outside them as not found.
// Every other tool error, including the outage text and an unexpected server
// error, is not a refusal.
func isAuthorizationRefusal(err error, entry string) bool {
	var statusError *httpStatusError
	if errors.As(err, &statusError) {
		return statusError.statusCode == http.StatusUnauthorized || statusError.statusCode == http.StatusForbidden
	}
	var toolError *toolCallError
	if !errors.As(err, &toolError) {
		return false
	}
	entryNotFound := fmt.Sprintf("reference %q: not found", entry)
	if strings.Contains(toolError.message, entryNotFound) {
		return true
	}
	if strings.Contains(toolError.message, searchAccessDeniedResponse) {
		return true
	}
	return strings.HasPrefix(toolError.message, permissionDeniedHeading)
}
