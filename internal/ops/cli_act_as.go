package ops

import (
	"context"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/service"
)

// actAsGroup holds the sanctioned way for an operator to make a product
// write as a named user. The write lands owned by the user, and its ledger
// row carries the operator, a recorded grant, and the reason, so nobody has
// to choose between recording the change as the user (losing the operator)
// or as the operator (losing the user) (TACK-424).
var actAsGroup = &clispec.Group{
	Use: "act-as", Short: "Make a recorded product write as a named user", Long: "", Parent: opsGroup,
}

type actAsCreateInput struct {
	clispec.InputMarker
	Email    string
	Reason   string
	ParentID string
	NodeType string
	Name     string
}

func actAsCreateOp(f *cli.Factory) clispec.Operation[actAsCreateInput] {
	return clispec.Operation[actAsCreateInput]{
		Name:     clispec.Name{Canonical: "create", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsActAsCreate), Mutates: true},
		Group:    actAsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Create one node as a named user, with the operator on the row",
		Long: "Creates one node under --parent as the user named by --user, who " +
			"must be an active user and a member of the org that holds the parent. " +
			"A grant row names the user, the org, and the reason; the node's own " +
			"ledger row keeps the user as its actor and carries the operator and " +
			"the grant id. Nothing writes without --execute; without it the command " +
			"reports the user and org it would write for.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[actAsCreateInput]{
			clispec.StringParam("user", "email of the user the write is made as", "", true,
				func(input *actAsCreateInput, value string) { input.Email = value }),
			clispec.StringParam("reason", "why the operator is acting as the user; recorded on the grant and the row", "", true,
				func(input *actAsCreateInput, value string) { input.Reason = value }),
			clispec.StringParam("parent", "UUID of the container the node is created under (its scope for numbering)", "", true,
				func(input *actAsCreateInput, value string) { input.ParentID = value }),
			clispec.StringParam("node-type", "type key of the node to create, such as issue", "", true,
				func(input *actAsCreateInput, value string) { input.NodeType = value }),
			clispec.StringParam("name", "name of the new node", "", true,
				func(input *actAsCreateInput, value string) { input.Name = value }),
		},
		New: func() actAsCreateInput {
			return actAsCreateInput{InputMarker: clispec.InputMarker{}, Email: "", Reason: "", ParentID: "", NodeType: "", Name: ""}
		},
		DryRun: func(ctx context.Context, input actAsCreateInput, sink clispec.ResultSink) error {
			return runActAsCreateWithEnv(ctx, f, input, sink, false)
		},
		Run: func(ctx context.Context, input actAsCreateInput, sink clispec.ResultSink) error {
			return runActAsCreateWithEnv(ctx, f, input, sink, true)
		},
	}
}

func runActAsCreateWithEnv(ctx context.Context, f *cli.Factory, input actAsCreateInput, sink clispec.ResultSink, execute bool) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return err
	}
	defer env.Close()
	nodes := service.NewNodeService(
		env.Stores.Nodes, env.Stores.Views, env.Stores.NodeTypes, env.Stores.PropertyDefs,
		env.Stores.Relationships, env.Stores.NodeDeleter,
	)
	deps := actAsDeps{
		outbox:   f.AuditOutbox(),
		identity: f.OperatorIdentitySource(),
		users:    postgres.NewUserRepo(env.Pool),
		members:  postgres.NewOrgMemberRepo(env.Pool),
		reader:   env.Stores.Views,
		nodes:    nodes,
	}
	return runActAsCreate(ctx, deps, input, sink, execute)
}
