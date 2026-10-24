package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

// errNotJSONObject reports a node type record that is not a JSON object.
var errNotJSONObject = errors.New("the record is not a JSON object")

// rawField is one top-level field of a JSON object with its stored bytes.
type rawField struct {
	name  string
	value json.RawMessage
}

// patchedNodeType returns stored with each top-level field that differs
// between before and after replaced by the field from after. It copies the
// bytes of every other stored field in stored order. It removes a changed
// field that after omits and appends, in after's order, a changed field that
// stored does not contain.
func patchedNodeType(ctx context.Context, stored, before, after []byte) ([]byte, error) {
	storedFields, err := orderedFields(ctx, "stored", stored)
	if err != nil {
		return nil, err
	}
	beforeFields, err := orderedFields(ctx, "expected", before)
	if err != nil {
		return nil, err
	}
	afterFields, err := orderedFields(ctx, "replacement", after)
	if err != nil {
		return nil, err
	}
	beforeValues, afterValues := fieldValues(beforeFields), fieldValues(afterFields)
	changed := map[string]bool{}
	for _, fields := range [][]rawField{beforeFields, afterFields} {
		for _, field := range fields {
			beforeValue, inBefore := beforeValues[field.name]
			afterValue, inAfter := afterValues[field.name]
			changed[field.name] = inBefore != inAfter || !bytes.Equal(beforeValue, afterValue)
		}
	}
	written := []rawField{}
	present := map[string]bool{}
	for _, field := range storedFields {
		present[field.name] = true
		if !changed[field.name] {
			written = append(written, field)
			continue
		}
		if value, inAfter := afterValues[field.name]; inAfter {
			written = append(written, rawField{name: field.name, value: value})
		}
	}
	for _, field := range afterFields {
		if changed[field.name] && !present[field.name] {
			written = append(written, field)
		}
	}
	return joinedFields(ctx, written)
}

// orderedFields decodes the top-level fields of the record in document order.
// It copies the exact bytes of each field value from document. A failure
// message includes role.
func orderedFields(ctx context.Context, role string, document []byte) ([]rawField, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	opening, err := decoder.Token()
	if err != nil {
		return nil, nodeOperationFailure(ctx, "read the opening token of the "+role+" node type", err)
	}
	if delimiter, isDelimiter := opening.(json.Delim); !isDelimiter || delimiter != '{' {
		return nil, nodeOperationFailure(ctx, "decode the "+role+" node type", errNotJSONObject)
	}
	fields := []rawField{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nodeOperationFailure(ctx, "read a field name of the "+role+" node type", err)
		}
		name, isName := token.(string)
		if !isName {
			return nil, nodeOperationFailure(ctx, "decode the "+role+" node type", errNotJSONObject)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nodeOperationFailure(ctx, "read field "+name+" of the "+role+" node type", err)
		}
		fields = append(fields, rawField{name: name, value: value})
	}
	return fields, nil
}

func fieldValues(fields []rawField) map[string]json.RawMessage {
	values := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		values[field.name] = field.value
	}
	return values
}

// joinedFields writes fields as one compact JSON object in the given order.
func joinedFields(ctx context.Context, fields []rawField) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			buffer.WriteByte(',')
		}
		name, err := json.Marshal(field.name)
		if err != nil {
			return nil, nodeOperationFailure(ctx, "encode node type field name "+field.name, err)
		}
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(field.value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}
