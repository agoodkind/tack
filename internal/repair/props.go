package repair

import (
	"encoding/json"

	"github.com/google/uuid"
)

func rawUUIDProp(props map[string]json.RawMessage, key string) uuid.UUID {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return uuid.Nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func stringProp(props map[string]json.RawMessage, key string) string {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func int64Prop(props map[string]json.RawMessage, key string) int64 {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return 0
	}
	var intValue int64
	if err := json.Unmarshal(raw, &intValue); err == nil {
		return intValue
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil {
		return int64(floatValue)
	}
	return 0
}
