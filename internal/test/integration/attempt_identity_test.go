package integration_test

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func hasMutationAttempt(rows []audit.Row, verb audit.Verb) bool {
	return slices.ContainsFunc(
		rows,
		func(row audit.Row) bool {
			return row.Action == string(verb) && row.Context.Phase == audit.PhaseAttempt &&
				row.Outcome == audit.OutcomeAttempted
		},
	)
}

func hasChange(rows []audit.Row) bool {
	return slices.ContainsFunc(
		rows,
		func(row audit.Row) bool { return row.Context.Phase == audit.PhaseChange },
	)
}

func requireMutationRequest(
	t *testing.T,
	ledger publicLedger,
	server *publicServer,
	requestID string,
	verb audit.Verb,
	tool string,
	reference string,
	target *audit.Target,
) {
	t.Helper()
	rows := ledger.requestRows(
		t,
		server.orgID,
		requestID,
		func(rows []audit.Row) bool {
			return hasMutationAttempt(rows, verb) && hasAuthAndOther(rows)
		},
	)
	auth := requireTokenUsedRow(t, rows, server.userID)
	requireMutationAttempt(
		t,
		rows,
		auth,
		requestID,
		verb,
		tool,
		reference,
		target,
		server.workspace,
	)
	if len(rows) != 2 {
		t.Fatalf(
			"request %s recorded %d events, want authentication and an attempt",
			requestID,
			len(rows),
		)
	}
}

func requireMutationAttempt(
	t *testing.T,
	rows []audit.Row,
	auth audit.Row,
	requestID string,
	verb audit.Verb,
	tool string,
	reference string,
	target *audit.Target,
	workspace string,
) {
	t.Helper()

	for _, row := range rows {
		if row.Action != string(verb) || row.Context.Phase != audit.PhaseAttempt {
			continue
		}

		context := row.Context
		if row.Outcome != audit.OutcomeAttempted || row.ActorID != auth.ActorID ||
			row.Delta != nil {
			t.Fatalf(
				"attempt %s has outcome %s, actor %s and delta %+v",
				row.EventID,
				row.Outcome,
				row.ActorID,
				row.Delta,
			)
		}

		if context.OperationClass != audit.OperationDatabaseChange ||
			context.OperationID != auth.Context.OperationID ||
			context.RequestID != requestID ||
			context.Tool != tool ||
			context.Target == nil {
			t.Fatalf(
				"mutation attempt %s actor = %s, outcome = %s, context = %+v",
				row.EventID,
				row.ActorID,
				row.Outcome,
				context,
			)
		}

		if context.TraceID != auth.Context.TraceID {
			t.Fatalf(
				"mutation attempt %s trace ID = %q, want authentication trace ID %q",
				row.EventID,
				context.TraceID,
				auth.Context.TraceID,
			)
		}

		if target != nil && *context.Target != *target {
			t.Fatalf(
				"mutation attempt %s target = %+v, want %+v",
				row.EventID,
				context.Target,
				target,
			)
		}

		if reference != "" &&
			(context.Selection == nil || context.Selection.Reference != reference) {
			t.Fatalf(
				"mutation attempt %s selection = %+v, want reference %s",
				row.EventID,
				context.Selection,
				reference,
			)
		}

		if target == nil && reference == "" {
			if context.Target.Kind != audit.TargetKindResource || context.Target.URI != "/mcp" ||
				context.Selection == nil || string(
				context.Selection.Criteria["workspace_reference"],
			) != "\""+workspace+"\"" {
				t.Fatalf(
					"mutation attempt %s target = %+v, selection = %+v",
					row.EventID,
					context.Target,
					context.Selection,
				)
			}
		}

		return
	}

	t.Fatalf("request %s did not record a %s attempt", requestID, verb)
}
