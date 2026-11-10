//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"slices"
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
	if want.skipItems {
		if row.Context.Read != nil {
			t.Fatalf(
				"failed read event %s stores read evidence %+v, want none",
				row.EventID,
				row.Context.Read,
			)
		}
		return
	}
	listed := env.ledger.manifestTargets(t, row)
	uris := make([]string, 0, len(listed))
	for _, listedTarget := range listed {
		uris = append(uris, listedTarget.URI)
	}
	if !want.contains {
		if !slices.Equal(uris, want.items) {
			t.Fatalf("read event %s read targets = %v, want %v", row.EventID, uris, want.items)
		}
		return
	}
	if len(uris) == 0 {
		t.Fatalf("read event %s lists no read targets, want at least one", row.EventID)
	}
	for _, uri := range uris {
		if !strings.HasPrefix(uri, want.itemPrefix) {
			t.Fatalf(
				"read event %s read target %q does not start with %q",
				row.EventID,
				uri,
				want.itemPrefix,
			)
		}
	}
	for _, item := range want.items {
		if !slices.Contains(uris, item) {
			t.Fatalf("read event %s read targets %v do not include %q", row.EventID, uris, item)
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
				row.Context.OperationClass == audit.OperationRead && row.Context.Phase == audit.PhaseRead {
				requireReadExpectation(t, env, row, expect(t, env))
				return
			}
		}
		t.Fatalf(
			"The ledger does not contain a %s read event of class read and phase read for the operator; the events are: %s",
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
