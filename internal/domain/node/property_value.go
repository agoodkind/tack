package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// propertyValueIssue checks raw against the shape and format of the type that
// definition declares. A type without a rule accepts any valid JSON value
// except null.
func propertyValueIssue(definition *PropertyDef, raw json.RawMessage) *PropertyIssue {
	if !json.Valid(raw) {
		return valueIssue(definition, PropertyIssueValue, "the value is not valid JSON")
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return valueIssue(definition, PropertyIssueValue, "the value is null")
	}
	switch definition.Type {
	case PropertyTypeText:
		return stringValueIssue(definition, raw, func(string) string { return "" })
	case PropertyTypeNumber:
		var number float64
		if json.Unmarshal(raw, &number) != nil {
			return valueIssue(definition, PropertyIssueValue, "the value must be a JSON number")
		}
		return nil
	case PropertyTypeCheckbox:
		var checked bool
		if json.Unmarshal(raw, &checked) != nil {
			return valueIssue(definition, PropertyIssueValue, "the value must be a JSON boolean")
		}
		return nil
	case PropertyTypeDate:
		return stringValueIssue(definition, raw, timeFormatReason(time.DateOnly))
	case PropertyTypeTimestamp:
		return stringValueIssue(definition, raw, timeFormatReason(time.RFC3339))
	case PropertyTypeURL:
		return stringValueIssue(definition, raw, urlReason)
	case PropertyTypeUUID:
		return stringValueIssue(definition, raw, uuidReason)
	case PropertyTypeSelect:
		return stringValueIssue(definition, raw, func(value string) string { return optionReason(definition, value) })
	case PropertyTypeMultiSelect:
		return multiSelectIssue(definition, raw)
	default:
		return nil
	}
}

func valueIssue(definition *PropertyDef, code PropertyIssueCode, reason string) *PropertyIssue {
	return &PropertyIssue{Property: definition.Name, Code: code, Reason: reason}
}

// stringValueIssue requires a JSON string and applies check to it. A
// nonempty result from check gives the rejection reason.
func stringValueIssue(definition *PropertyDef, raw json.RawMessage, check func(string) string) *PropertyIssue {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return valueIssue(definition, PropertyIssueValue, "the value must be a JSON string")
	}
	reason := check(value)
	if reason == "" {
		return nil
	}
	code := PropertyIssueValue
	if definition.Type == PropertyTypeSelect {
		code = PropertyIssueOption
	}
	return valueIssue(definition, code, reason)
}

func timeFormatReason(layout string) func(string) string {
	return func(value string) string {
		if _, err := time.Parse(layout, value); err != nil {
			return fmt.Sprintf("the value %q does not match the layout %s", value, layout)
		}
		return ""
	}
}

func urlReason(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Sprintf("the value %q is not an absolute URL", value)
	}
	return ""
}

func uuidReason(value string) string {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Sprintf("the value %q is not a canonical UUID", value)
	}
	return ""
}

// optionReason accepts only an option key. When value matches an option
// label, the reason includes that option's key.
func optionReason(definition *PropertyDef, value string) string {
	for _, option := range definition.Options {
		if option.Key == value {
			return ""
		}
	}
	for _, option := range definition.Options {
		if option.Label == value {
			return fmt.Sprintf("the value %q is the label of option key %q", value, option.Key)
		}
	}
	return fmt.Sprintf("the value %q is not an option key", value)
}

func multiSelectIssue(definition *PropertyDef, raw json.RawMessage) *PropertyIssue {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return valueIssue(definition, PropertyIssueValue, "the value must be a JSON array of strings")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return valueIssue(definition, PropertyIssueOption, fmt.Sprintf("the option key %q appears more than once", value))
		}
		seen[value] = struct{}{}
		if reason := optionReason(definition, value); reason != "" {
			return valueIssue(definition, PropertyIssueOption, reason)
		}
	}
	return nil
}
