package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain"
)

// ErrSchemaRead reports a schema condition that prevents interpretation from
// completing.
var ErrSchemaRead = errors.New("schema read incomplete")

// PropertyIssueCode classifies why the schema rejects a property.
type PropertyIssueCode string

const (
	// PropertyIssueUndeclared marks a property name without a definition.
	PropertyIssueUndeclared PropertyIssueCode = "undeclared"
	// PropertyIssueNotApplicable marks a definition that does not apply to the
	// node type.
	PropertyIssueNotApplicable PropertyIssueCode = "not_applicable"
	// PropertyIssueValue marks a value with the wrong JSON shape or format.
	PropertyIssueValue PropertyIssueCode = "invalid_value"
	// PropertyIssueOption marks a select value that does not match an option key.
	PropertyIssueOption PropertyIssueCode = "unknown_option"
)

// PropertyIssue records a property that the schema rejects and its reason.
type PropertyIssue struct {
	Property string
	Code     PropertyIssueCode
	Reason   string
}

// ReadFailure reports a schema condition that prevents interpretation from
// completing.
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

// Error lists only read failures when the result contains any.
// Otherwise, it lists every invalid property with its reason.
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
func (e PropertyResultError) Unwrap() error {
	if len(e.Result.Failures) > 0 {
		return ErrSchemaRead
	}
	return domain.ErrInvalidArgument
}
