package clispec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

// InputReadSpec returns a read definition for a command with typed input.
//
// The definition decodes the command input before calling target or
// safe. It uses the target result as the sole operation target and
// encodes the value returned by safe as the stored input.
//
//	spec := InputReadSpec(verb, target, safe)
func InputReadSpec[Typed Input](verb audit.Verb, target func(Typed) audit.Target, safe func(Typed) Typed) audit.Spec {
	decode := func(encoded json.RawMessage) (Typed, error) {
		var decoded Typed
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			slog.Error("clispec.read_input_failed", slog.String("verb", string(verb)), slog.String("err", err.Error()))
			return decoded, fmt.Errorf("%s: decode the command input: %w", verb, err)
		}
		return decoded, nil
	}
	return audit.Spec{
		Verb: string(verb), Reads: true, Class: audit.OperationRead,
		Targets: func(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
			input, err := decode(encoded)
			if err != nil {
				return nil, err
			}
			return []audit.Target{target(input)}, nil
		},
		SafeInput: func(encoded json.RawMessage) (json.RawMessage, error) {
			input, err := decode(encoded)
			if err != nil {
				return nil, err
			}
			safeEncoded, err := json.Marshal(safe(input))
			if err != nil {
				slog.Error("clispec.read_input_failed", slog.String("verb", string(verb)), slog.String("err", err.Error()))
				return nil, fmt.Errorf("%s: encode the safe input: %w", verb, err)
			}
			return safeEncoded, nil
		},
	}
}
