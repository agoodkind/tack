package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// recordPropertiesField identifies the record member that stores the property
// map. The changed field list compares each property separately.
const recordPropertiesField = "props"

// recordBookkeepingFields lists the record members that the changed field
// list omits. The event actor, context, and occurrence time record these
// values.
var recordBookkeepingFields = []string{"id", "org_id", "node_type", "created_at", "created_by", "updated_at", "updated_by"}

// recordField identifies a top-level record member or, when isProperty is
// true, a property in the property map.
type recordField struct {
	member     string
	property   string
	isProperty bool
}

// pointer returns the field's RFC 6901 JSON pointer within the record.
func (f recordField) pointer() string {
	escape := strings.NewReplacer("~", "~0", "/", "~1")
	if !f.isProperty {
		return "/" + escape.Replace(f.member)
	}
	return "/" + escape.Replace(f.member) + "/" + escape.Replace(f.property)
}

// NewChange returns the change of one record with its complete old and new
// encodings. A nil or empty encoding identifies an absent record. Changed
// lists each changed field as an RFC 6901 JSON pointer in sorted order.
func NewChange(eventID uuid.UUID, target Target, before, after json.RawMessage) (Change, error) {
	changed, err := changedFields(before, after)
	if err != nil {
		return Change{}, err
	}
	change := Change{EventID: eventID, Target: target, Before: before, After: after, Changed: changed}
	if err := ValidateChange(change); err != nil {
		slog.Error("audit.mutation_event_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return Change{}, err
	}
	return change, nil
}

// changedFields compares two record encodings and returns the JSON pointer
// of each field that differs or exists on one side only.
func changedFields(source, result json.RawMessage) ([]string, error) {
	before, err := recordFields(source)
	if err != nil {
		return nil, err
	}
	after, err := recordFields(result)
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for field, value := range before {
		if other, ok := after[field]; ok && bytes.Equal(value, other) {
			continue
		}
		changed = append(changed, field.pointer())
	}
	for field := range after {
		if _, existed := before[field]; !existed {
			changed = append(changed, field.pointer())
		}
	}
	slices.Sort(changed)
	return changed, nil
}

// recordFields returns the fields to compare from one record encoding, with
// each value in compact JSON. A nil or empty encoding returns an empty set.
func recordFields(encoding json.RawMessage) (map[recordField]json.RawMessage, error) {
	fields := map[recordField]json.RawMessage{}
	if len(encoding) == 0 {
		return fields, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoding, &members); err != nil {
		return nil, mutationEventFailure("decode the record members", err)
	}
	for member, value := range members {
		if slices.Contains(recordBookkeepingFields, member) {
			continue
		}
		if member != recordPropertiesField {
			if err := addRecordField(fields, recordField{member: member, property: "", isProperty: false}, value); err != nil {
				return nil, err
			}
			continue
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(value, &properties); err != nil {
			return nil, mutationEventFailure("decode the record properties", err)
		}
		for property, propertyValue := range properties {
			if err := addRecordField(fields, recordField{member: member, property: property, isProperty: true}, propertyValue); err != nil {
				return nil, err
			}
		}
	}
	return fields, nil
}

func addRecordField(fields map[recordField]json.RawMessage, field recordField, value json.RawMessage) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return mutationEventFailure("compact the value of "+field.pointer(), err)
	}
	fields[field] = compact.Bytes()
	return nil
}

func mutationEventFailure(operation string, err error) error {
	slog.Error("audit.mutation_event_failed", slog.String("operation", operation), slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", operation, err)
}
