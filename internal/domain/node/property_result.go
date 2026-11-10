package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain"
)

// ErrSchemaRead reports that interpretation lacks a required schema record.
var ErrSchemaRead = errors.New("schema read incomplete")

// PropertyIssueCode classifies a property value that the schema rejects.
type PropertyIssueCode string

const (
	// PropertyIssueUndeclared reports a property name without a definition.
	PropertyIssueUndeclared PropertyIssueCode = "undeclared"
	// PropertyIssueNotApplicable reports a definition that does not apply to the
	// node type.
	PropertyIssueNotApplicable PropertyIssueCode = "not_applicable"
	// PropertyIssueValue reports a value with the wrong JSON shape or format.
	PropertyIssueValue PropertyIssueCode = "invalid_value"
	// PropertyIssueOption reports a select value that does not match an option
	// key.
	PropertyIssueOption PropertyIssueCode = "unknown_option"
)

// PropertyIssue records a property that the schema rejects and its reason.
type PropertyIssue struct {
	Property string
	Code     PropertyIssueCode
	Reason   string
}

// ReadFailure reports a required schema record that interpretation did not
// receive.
type ReadFailure struct {
	Subject string
	Reason  string
}

// PropertyResult records the interpretation of one node's properties.
// Valid maps each accepted property name to its value. Invalid lists
// properties that the schema rejects. Failures lists schema read failures.
type PropertyResult struct {
	NodeType string
	Valid    map[string]json.RawMessage
	Invalid  []PropertyIssue
	Failures []ReadFailure
}

// Err returns nil for a valid result. Otherwise, it returns a
// [PropertyResultError] for the result.
//
// A valid result contains neither read failures nor invalid properties.
//
//   - The returned error preserves the complete result for inspection.
func (r PropertyResult) Err() error {
	if len(r.Failures) == 0 && len(r.Invalid) == 0 {
		return nil
	}
	return PropertyResultError{Result: r}
}

// PropertyResultError reports read failures or invalid properties in a
// [PropertyResult]. It matches [ErrSchemaRead] when the result contains a
// read failure and [domain.ErrInvalidArgument] otherwise.
type PropertyResultError struct {
	Result PropertyResult
}

// Error lists only read failures when the result contains any. Otherwise,
// it lists every invalid property with its reason.
//
// The message includes the node type and the error classification.
//
//   - A semicolon separates multiple failure or invalid-property entries.
func (e PropertyResultError) Error() string {
	if len(e.Result.Failures) > 0 {
		parts := make([]string, 0, len(e.Result.Failures))
		for _, failure := range e.Result.Failures {
			parts = append(parts, failure.Subject+": "+failure.Reason)
		}
		return fmt.Sprintf("interpret node type %q properties: %s: %s", e.Result.NodeType, strings.Join(parts, "; "), ErrSchemaRead.Error())
	}
	parts := make([]string, 0, len(e.Result.Invalid))
	for _, issue := range e.Result.Invalid {
		parts = append(parts, fmt.Sprintf("properties.%s: %s", issue.Property, issue.Reason))
	}
	return fmt.Sprintf("node type %q properties are invalid: %s: %s", e.Result.NodeType, strings.Join(parts, "; "), domain.ErrInvalidArgument.Error())
}

// Unwrap returns [ErrSchemaRead] when the result contains a read failure and
// [domain.ErrInvalidArgument] otherwise.
//
// The returned error supports [errors.Is] without requiring message parsing.
//
//   - An empty PropertyResultError also unwraps to domain.ErrInvalidArgument.
func (e PropertyResultError) Unwrap() error {
	if len(e.Result.Failures) > 0 {
		return ErrSchemaRead
	}
	return domain.ErrInvalidArgument
}
