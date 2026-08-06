package datagen

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type httpStatusError struct {
	operation  string
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf(
		"qa datagen: %s returned HTTP %d: %s",
		e.operation,
		e.statusCode,
		e.body,
	)
}

func (e *rpcError) Error() string {
	return fmt.Sprintf(
		"qa datagen: %s JSON-RPC error %d: %s",
		e.toolName,
		e.Code,
		e.Message,
	)
}

type toolCallError struct {
	toolName string
	message  string
}

func (e *toolCallError) Error() string {
	return fmt.Sprintf("qa datagen: %s tool error: %s", e.toolName, e.message)
}

func isAuthenticationRejection(err error) bool {
	var statusError *httpStatusError
	return errors.As(err, &statusError) &&
		statusError.statusCode == http.StatusUnauthorized
}

func isToolUnavailable(err error) bool {
	var responseError *rpcError
	if errors.As(err, &responseError) && responseError.Code == -32601 {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"tool not found",
		"unknown tool",
		"not registered",
		"method not found",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
