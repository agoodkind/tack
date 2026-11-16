package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// Token commands issue, list, and revoke credentials after the initial seed
// (TACK-472). Each command records the operator through the shared audit
// mechanism. Creation prints the raw token once and never logs it.

type authTokenCreateInput struct {
	clispec.InputMarker
	Email string
	Label string
}

type authTokenListInput struct {
	clispec.InputMarker
	Email string
}

type authTokenRevokeInput struct {
	clispec.InputMarker
	TokenID string
}

func authTokenCreateOp(f *cli.Factory) clispec.Operation[authTokenCreateInput] {
	return clispec.Operation[authTokenCreateInput]{
		Name:     clispec.Name{Canonical: "token-create", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsAuthTokenCreate), Mutates: true},
		Group:    authOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Create an API token for an existing user and print it once",
		Long: "Create one bearer token for the user specified by --email. " +
			"The command stores only its hash, records issuance in the ledger, " +
			"and prints the raw value exactly once. The command requires --execute " +
			"to write. Without it, the command reports the intended user.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenCreateInput]{
			clispec.StringParam(
				"email",
				"email of the existing user to issue the token to",
				"",
				true,
				func(input *authTokenCreateInput, value string) { input.Email = value },
			),
			clispec.StringParam(
				"label",
				"label for the client that will use the token",
				"",
				true,
				func(input *authTokenCreateInput, value string) { input.Label = value },
			),
		},
		New: func() authTokenCreateInput {
			return authTokenCreateInput{InputMarker: clispec.InputMarker{}, Email: "", Label: ""}
		},
		DryRun: func(ctx context.Context, input authTokenCreateInput, sink clispec.ResultSink) error {
			return runAuthTokenCreate(ctx, f, input, sink, false)
		},
		Run: func(ctx context.Context, input authTokenCreateInput, sink clispec.ResultSink) error {
			return runAuthTokenCreate(ctx, f, input, sink, true)
		},
	}
}

func authTokenListOp(f *cli.Factory) clispec.Operation[authTokenListInput] {
	return clispec.Operation[authTokenListInput]{
		Name:     clispec.Name{Canonical: "token-list", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    singletonReadSpec(audit.VerbOpsAuthTokenList, "auth/token-list"),
		Group:    authOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "List a user's API tokens by id, label, and last use",
		Long: "Read every token issued to the user specified by --email. " +
			"The database does not store raw values, and the command does not " +
			"show them. Use token-create to create a token.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenListInput]{
			clispec.StringParam(
				"email",
				"email of the user to list tokens for",
				"",
				true,
				func(input *authTokenListInput, value string) { input.Email = value },
			),
		},
		New: func() authTokenListInput {
			return authTokenListInput{InputMarker: clispec.InputMarker{}, Email: ""}
		},
		Run: func(ctx context.Context, input authTokenListInput, sink clispec.ResultSink) error {
			return runAuthTokenList(ctx, f, input, sink)
		},
	}
}

func authTokenRevokeOp(f *cli.Factory) clispec.Operation[authTokenRevokeInput] {
	return clispec.Operation[authTokenRevokeInput]{
		Name:     clispec.Name{Canonical: "token-revoke", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsAuthTokenRevoke), Mutates: true},
		Group:    authOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Revoke one API token by id",
		Long: "Delete the token row specified by --id and record the revocation. " +
			"Authentication rejects its bearer value on subsequent requests. " +
			"The command requires --execute to write. Without it, the command " +
			"reports the token it would revoke.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenRevokeInput]{
			clispec.StringParam(
				"id",
				"id of the token to revoke, from token-list or token-create",
				"",
				true,
				func(input *authTokenRevokeInput, value string) { input.TokenID = value },
			),
		},
		New: func() authTokenRevokeInput {
			return authTokenRevokeInput{InputMarker: clispec.InputMarker{}, TokenID: ""}
		},
		DryRun: func(ctx context.Context, input authTokenRevokeInput, sink clispec.ResultSink) error {
			return runAuthTokenRevoke(ctx, f, input, sink, false)
		},
		Run: func(ctx context.Context, input authTokenRevokeInput, sink clispec.ResultSink) error {
			return runAuthTokenRevoke(ctx, f, input, sink, true)
		},
	}
}
