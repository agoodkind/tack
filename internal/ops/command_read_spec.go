package ops

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
)

// readSpec returns an audit specification for an operator read of the
// external target reference. SafeInput returns the command input unchanged.
// Callers must include only flag values that do not contain credentials.
func readSpec(verb audit.Verb, reference string) audit.Spec {
	target := externalTarget(reference)
	return audit.Spec{
		Verb: string(verb), Reads: true, Class: audit.OperationRead,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{target}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}
