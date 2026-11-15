//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

type readExpectation struct {
	target     readRowTarget
	input      string
	items      []string
	contains   bool
	itemPrefix string
	absent     []string
	skipItems  bool
}

func normalizedJSONObject(t *testing.T, raw []byte) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode the JSON object %s: %v", raw, err)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the JSON object %s: %v", raw, err)
	}
	return string(normalized)
}

func requireReadExpectation(t *testing.T, env *coverageEnv, row audit.Row, want readExpectation) {
	t.Helper()
	target := row.Context.Target
	if target == nil || target.URI != want.target.uri || target.Reference != want.target.reference {
		t.Fatalf("read event %s target = %+v, want %+v", row.EventID, target, want.target)
	}
	var extra struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the extra of read event %s: %v\n%s", row.EventID, err, row.Extra)
	}
	storedInput := ""
	if len(extra.Input) != 0 && string(extra.Input) != "null" {
		storedInput = normalizedJSONObject(t, extra.Input)
	}
	wantInput := ""
	if want.input != "" {
		wantInput = normalizedJSONObject(t, []byte(want.input))
	}
	if storedInput != wantInput {
		t.Fatalf("read event %s stored input = %s, want %s", row.EventID, storedInput, wantInput)
	}
	for _, text := range want.absent {
		if strings.Contains(string(row.Extra), text) {
			t.Fatalf(
				"read event %s stores the text %q in its extra: %s",
				row.EventID,
				text,
				row.Extra,
			)
		}
	}
}

func withReadCheck(
	base coverageCase,
	verb audit.Verb,
	outcome audit.Outcome,
	expect func(*testing.T, *coverageEnv) readExpectation,
) coverageCase {
	base.check = func(t *testing.T, env *coverageEnv, rows []audit.Row) {
		t.Helper()
		for _, row := range rows {
			if row.Action == string(verb) && row.Outcome == outcome &&
				row.Context.OperationClass == audit.OperationRead && row.Context.Phase == audit.PhaseAttempt {
				requireReadExpectation(t, env, row, expect(t, env))
				return
			}
		}
		t.Fatalf(
			"The ledger does not contain a %s read event of class read and phase attempt for the operator; the events are: %s",
			outcome,
			describeRows(rows),
		)
	}
	return base
}

func fixedReadExpectation(reference string) func(*testing.T, *coverageEnv) readExpectation {
	return func(*testing.T, *coverageEnv) readExpectation {
		return readExpectation{
			target:     referenceTarget(reference),
			input:      "",
			items:      nil,
			contains:   false,
			itemPrefix: "",
			absent:     nil,
			skipItems:  false,
		}
	}
}

func failedReadExpectation(reference string) func(*testing.T, *coverageEnv) readExpectation {
	return func(*testing.T, *coverageEnv) readExpectation {
		return readExpectation{
			target:     referenceTarget(reference),
			input:      "",
			items:      nil,
			contains:   false,
			itemPrefix: "",
			absent:     nil,
			skipItems:  true,
		}
	}
}
