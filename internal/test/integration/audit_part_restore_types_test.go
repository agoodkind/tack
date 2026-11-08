package integration_test

import "strings"

const (
	wrongTypeFragment    = "has the wrong JSON type"
	listElementFragment  = "has an element that is not a string"
	notUUIDFragment      = "is not a UUID"
	wrongScalarValue     = "7"
	wrongContainerValue  = `"text"`
	wrongNumberValue     = `"2"`
	invalidUUIDValue     = `"not-a-uuid"`
	partRestoreStringSet = "actor.type actor.id actor.email actor.name actor.session_id actor.ip actor.ua actor.request_id " +
		"actor.api_token_label entity.type entity.node_type entity.id entity.identifier entity.name " +
		"context.org_id context.workspace_id context.scope_id context.parent_id context.request_id context.trace_id " +
		"context.source context.tool context.rpc context.reason context.api_token_id context.commit_version " +
		"context.operation_class context.phase context.operation_id context.parent_operation_id context.started_at " +
		"context.completed_at context.http_method context.request_path context.target.kind context.target.node_type " +
		"context.target.reference context.target.name context.target.uri context.target.relation_type context.target.id " +
		"context.target.source_id context.target.target_id context.target.owner_id verb event_id outcome idempotency_key " +
		"occurred_at protected.0.ref protected.0.subject_id"
	partRestoreContainerSet = "actor entity context delta error context.target context.read context.selection " +
		"context.committed_steps delta.changed protected"
	partRestoreUUIDSet = "actor.id entity.id context.workspace_id context.scope_id context.parent_id context.api_token_id " +
		"context.parent_operation_id context.target.id context.target.source_id context.target.target_id " +
		"context.target.owner_id protected.0.ref protected.0.subject_id"
)

const (
	validTimeFragment    = "valid occurrence, start, or completion time"
	nilEventIDFragment   = "change event ID is nil"
	integerFragment      = "is not written as an integer"
	selectionFragment    = "includes read evidence or a selection"
	nilUUIDValue         = `"00000000-0000-0000-0000-000000000000"`
	decimalFormatVersion = "2.0"
)

func contractGapMutations() []partRestoreMutation {
	nilEvent := headerSet("nil event ID", nilEventIDFragment, "event_id", nilUUIDValue)
	nilEvent.nilID = true
	return []partRestoreMutation{
		nilEvent,
		headerSet("decimal context.audit_format_version", integerFragment, "context.audit_format_version", decimalFormatVersion),
		headerSet("impossible calendar date", "does not decode", "occurred_at", `"2026-02-30T00:00:00Z"`),
		headerSet("hour 24", validTimeFragment, "occurred_at", `"2026-01-01T24:00:00Z"`),
		headerSet("second 60", validTimeFragment, "occurred_at", `"2026-01-01T23:59:60Z"`),
		headerSet("selection with an unknown member", selectionFragment, "context.selection", `{"unknown":1}`),
		headerSet("selection with a known member", selectionFragment, "context.selection", `{"reference":"issue"}`),
	}
}

func wrongTypeMutations() []partRestoreMutation {
	mutations := []partRestoreMutation{}
	for _, path := range strings.Fields(partRestoreStringSet) {
		mutations = append(mutations, headerSet("number in "+path, wrongTypeFragment, path, wrongScalarValue))
	}
	for _, path := range strings.Fields(partRestoreContainerSet) {
		mutations = append(mutations, headerSet("text in "+path, wrongTypeFragment, path, wrongContainerValue))
	}
	for _, path := range strings.Fields(partRestoreUUIDSet) {
		mutations = append(mutations, headerSet("invalid UUID in "+path, notUUIDFragment, path, invalidUUIDValue))
	}
	return append(mutations,
		headerSet("text in context.audit_format_version", wrongTypeFragment, "context.audit_format_version", wrongNumberValue),
		headerSet("number in context.committed_steps", listElementFragment, "context.committed_steps", "[1]"),
		headerSet("number in delta.changed", listElementFragment, "delta.changed", "[1]"),
	)
}
