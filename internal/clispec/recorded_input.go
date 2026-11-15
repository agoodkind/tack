package clispec

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
)

type recordedInputKey struct{}

func inputContext[I Input](ctx context.Context, verb string, input I) (context.Context, error) {
	if verb != string(audit.VerbAuditExport) && verb != string(audit.VerbOpsAuthTokenRevoke) {
		return ctx, nil
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, loggedAuditError(ctx, "encode command audit input", err)
	}

	selected, err := audit.SelectCommandInput(audit.Verb(verb), encoded)
	if err != nil {
		return nil, loggedAuditError(ctx, "select command audit input", err)
	}

	return context.WithValue(ctx, recordedInputKey{}, selected), nil
}

func recordedInput(ctx context.Context) json.RawMessage {
	input, _ := ctx.Value(recordedInputKey{}).(json.RawMessage)

	return input
}
