package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// An environment past its first seed had no way to issue a credential: the
// seed refuses to run again once an org exists, and tokens are stored only as
// hashes, so no raw value is recoverable. Production could therefore never
// leave development auth, where the bearer is the raw user id (TACK-472).
// These commands are the missing half of the token lifecycle: issue, list, and
// revoke, each recorded through the operator choke-point like every other
// command, with the issued value printed exactly once and never logged.

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
		Name:    clispec.Name{Canonical: "token-create", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsAuthTokenCreate), Mutates: true},
		Group:   authOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Mint an API token for an existing user and print it once",
		Long: "Creates one bearer token for the user named by --email, stores only " +
			"its hash, records the issue in the ledger, and prints the raw value " +
			"exactly once. Nothing writes without --execute; without it the command " +
			"reports the user it would issue for.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenCreateInput]{
			clispec.StringParam("email", "email of the existing user the token is issued to", "", true,
				func(input *authTokenCreateInput, value string) { input.Email = value }),
			clispec.StringParam("label", "label stored with the token, naming the client that will hold it", "", true,
				func(input *authTokenCreateInput, value string) { input.Label = value }),
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
		Name:    clispec.Name{Canonical: "token-list", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsAuthTokenList), Reads: true},
		Group:   authOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "List a user's API tokens by id, label, and last use",
		Long: "Reads every token issued to the user named by --email. Raw values " +
			"are never stored, so none is shown; use token-create for a new one.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenListInput]{
			clispec.StringParam("email", "email of the user whose tokens to list", "", true,
				func(input *authTokenListInput, value string) { input.Email = value }),
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
		Name:    clispec.Name{Canonical: "token-revoke", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsAuthTokenRevoke), Mutates: true},
		Group:   authOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Revoke one API token by id",
		Long: "Deletes the token row named by --id, so the bearer it hashed to is " +
			"refused from the next request on, and records the revocation. Nothing " +
			"writes without --execute; without it the command reports the token it " +
			"would revoke.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[authTokenRevokeInput]{
			clispec.StringParam("id", "id of the token to revoke, from token-list or token-create", "", true,
				func(input *authTokenRevokeInput, value string) { input.TokenID = value }),
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
