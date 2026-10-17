package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// recordPropertiesField identifies the primary record member that stores the
// property map. The delta compares each property separately.
const recordPropertiesField = "props"

// recordBookkeepingFields lists the primary record members that the delta
// omits. The event entity, context, actor, and occurrence time already record
// these values.
var recordBookkeepingFields = []string{"id", "org_id", "node_type", "created_at", "created_by", "updated_at", "updated_by"}

var errMutationRecordMissing = errors.New("the event needs the record that the transaction read or wrote")

// recordField identifies a top-level primary record member or, when
// isProperty is true, a property in the property map.
type recordField struct {
	member     string
	property   string
	isProperty bool
}

// pointer returns the field's RFC 6901 JSON pointer within the primary record.
func (f recordField) pointer() string {
	escape := strings.NewReplacer("~", "~0", "/", "~1")
	if !f.isProperty {
		return "/" + escape.Replace(f.member)
	}
	return "/" + escape.Replace(f.member) + "/" + escape.Replace(f.property)
}

// FinalizeMutationEvent builds an event from template and the primary record
// encodings in source and result. It sets the delta and preserves every other
// template field, including the event ID and occurrence time.
func FinalizeMutationEvent(template, source, result json.RawMessage) (json.RawMessage, error) {
	var event Event
	if err := json.Unmarshal(template, &event); err != nil {
		return nil, mutationEventFailure("decode the staged audit event", err)
	}
	if err := requireMutationRecords(Verb(event.Verb), source, result); err != nil {
		return nil, err
	}
	if len(source) == 0 && len(result) == 0 {
		return template, nil
	}
	delta, err := recordDelta(source, result)
	if err != nil {
		return nil, err
	}
	event.Delta = delta
	return MarshalEvent(event)
}

func mutationEventFailure(operation string, err error) error {
	slog.Error("audit.mutation_event_failed", slog.String("operation", operation), slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", operation, err)
}

// requireMutationRecords rejects a node.create event without a result
// encoding, a node.update event without both encodings, or a node.delete
// event without a source encoding. A nil or empty encoding indicates an
// absent record. Other verbs permit both encodings to be absent.
func requireMutationRecords(verb Verb, source, result json.RawMessage) error {
	needSource := verb == VerbNodeUpdate || verb == VerbNodeDelete
	needResult := verb == VerbNodeUpdate || verb == VerbNodeCreate
	if needSource && len(source) == 0 {
		return mutationEventFailure(string(verb)+" source record", errMutationRecordMissing)
	}
	if needResult && len(result) == 0 {
		return mutationEventFailure(string(verb)+" result record", errMutationRecordMissing)
	}
	return nil
}

// recordDelta compares two primary record encodings. Before and After contain
// only changed fields in the primary record's shape. Changed lists the changed
// fields as RFC 6901 JSON pointers in sorted order. A field that exists on
// only one side appears only on that side.
func recordDelta(source, result json.RawMessage) (*Delta, error) {
	before, err := recordFields(source)
	if err != nil {
		return nil, err
	}
	after, err := recordFields(result)
	if err != nil {
		return nil, err
	}
	changedBefore := map[recordField]json.RawMessage{}
	changedAfter := map[recordField]json.RawMessage{}
	changed := []string{}
	for field, value := range before {
		if other, ok := after[field]; ok && bytes.Equal(value, other) {
			continue
		}
		changedBefore[field] = value
		changed = append(changed, field.pointer())
	}
	for field, value := range after {
		previous, existed := before[field]
		if existed && bytes.Equal(value, previous) {
			continue
		}
		changedAfter[field] = value
		if !existed {
			changed = append(changed, field.pointer())
		}
	}
	slices.Sort(changed)
	beforeJSON, err := partialRecord(changedBefore)
	if err != nil {
		return nil, err
	}
	afterJSON, err := partialRecord(changedAfter)
	if err != nil {
		return nil, err
	}
	return &Delta{Before: beforeJSON, After: afterJSON, Changed: changed}, nil
}

// recordFields returns the fields to compare from one primary record
// encoding, with each value in compact JSON. A nil or empty encoding
// represents an absent record and yields an empty field set.
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

// partialRecord encodes fields in the primary record's shape. It returns nil
// when fields is empty.
func partialRecord(fields map[recordField]json.RawMessage) (json.RawMessage, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	members := map[string]json.RawMessage{}
	properties := map[string]json.RawMessage{}
	for field, value := range fields {
		if field.isProperty {
			properties[field.property] = value
			continue
		}
		members[field.member] = value
	}
	if len(properties) > 0 {
		encoded, err := json.Marshal(properties)
		if err != nil {
			return nil, mutationEventFailure("encode the changed properties", err)
		}
		members[recordPropertiesField] = encoded
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return nil, mutationEventFailure("encode the changed fields", err)
	}
	return encoded, nil
}
