//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// coverageLegacyProperty is the property key that repair cases add to a node
// and remove through a repair profile.
const coverageLegacyProperty = "coverage_legacy_note"

func nodeReadExpectation(nodeID uuid.UUID, inputFormat string) readExpectation {
	nodeURI := audit.NodeURIPrefix + nodeID.String()
	return readExpectation{
		target:     uriTarget(nodeURI),
		input:      fmt.Sprintf(inputFormat, nodeID.String()),
		items:      []string{nodeURI},
		contains:   false,
		itemPrefix: "",
		absent:     nil,
		skipItems:  false,
	}
}

// addNodeCommandCases adds node inspection, validation, repair, creation, and
// batch command cases with their expected audit verbs and outcomes.
func addNodeCommandCases(cases map[string]coverageCase) {
	cases["command ops inspect read"] = withReadCheck(
		commandCase(
			audit.VerbOpsInspectRead,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				return commandInput{
					args: []string{
						"ops",
						"inspect",
						"read",
						"--node",
						env.fixture(t).nodes["issue"].String(),
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsInspectRead,
		audit.OutcomeCompleted,
		func(t *testing.T, env *coverageEnv) readExpectation {
			return nodeReadExpectation(
				env.fixture(t).nodes["issue"],
				`{"NodeID":%q,"Records":false}`,
			)
		},
	)
	cases["command ops inspect find"] = withReadCheck(
		commandCase(
			audit.VerbOpsInspectFind,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				return commandInput{
					args: []string{
						"ops",
						"inspect",
						"find",
						"--org",
						env.server.orgID.String(),
						"--type",
						"issue",
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsInspectFind,
		audit.OutcomeCompleted,
		func(t *testing.T, env *coverageEnv) readExpectation {
			return readExpectation{
				target: uriTarget(audit.OrgURIPrefix + env.server.orgID.String()),
				input: fmt.Sprintf(
					`{"OrgID":%q,"NodeType":"issue","Limit":50}`,
					env.server.orgID.String(),
				),
				items: []string{
					audit.NodeURIPrefix + env.fixture(t).nodes["issue"].String(),
				},
				contains:   true,
				itemPrefix: audit.NodeURIPrefix,
				absent:     nil,
				skipItems:  false,
			}
		},
	)
	cases["command ops inspect query"] = withReadCheck(
		commandCase(
			audit.VerbOpsInspectQuery,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				return commandInput{
					args: []string{
						"ops",
						"inspect",
						"query",
						"--org",
						env.server.orgID.String(),
						"--type",
						"workspace",
						"--property",
						"slug",
						"--value",
						strconv.Quote(env.server.workspace),
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsInspectQuery,
		audit.OutcomeCompleted,
		func(_ *testing.T, env *coverageEnv) readExpectation {
			workspaceID := node.WorkspaceID(env.server.orgID, env.server.workspace)
			return readExpectation{
				target: uriTarget(audit.OrgURIPrefix + env.server.orgID.String()),
				input: fmt.Sprintf(
					`{"OrgID":%q,"NodeType":"workspace","Property":"slug","Value":"","Limit":50}`,
					env.server.orgID.String(),
				),
				items: []string{
					audit.NodeURIPrefix + workspaceID.String(),
				},
				contains:   true,
				itemPrefix: audit.NodeURIPrefix,
				absent:     []string{env.server.workspace},
				skipItems:  false,
			}
		},
	)
	cases["command ops verify node"] = withReadCheck(
		commandCase(
			audit.VerbOpsVerifyNode,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				return commandInput{
					args: []string{
						"ops",
						"verify",
						"node",
						"--node",
						env.fixture(t).nodes["issue"].String(),
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsVerifyNode,
		audit.OutcomeCompleted,
		func(t *testing.T, env *coverageEnv) readExpectation {
			return nodeReadExpectation(
				env.fixture(t).nodes["issue"],
				`{"NodeID":%q,"Class":"","Profile":"","Records":false}`,
			)
		},
	)
	cases["command ops repair classes"] = withReadCheck(
		commandCase(
			audit.VerbOpsRepairClasses,
			audit.OutcomeCompleted,
			fixedCommand("ops", "repair", "classes"),
		),
		audit.VerbOpsRepairClasses,
		audit.OutcomeCompleted,
		fixedReadExpectation("repair/classes"),
	)
	var validatedNode uuid.UUID
	cases["command ops validate node"] = withReadCheck(
		commandCase(
			audit.VerbOpsValidateNode,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				nodeID, profile := env.legacyRepair(t)
				validatedNode = nodeID
				return commandInput{
					args: []string{
						"ops",
						"validate",
						"node",
						"--node",
						nodeID.String(),
						"--class",
						string(ops.RepairClassPropsTransform),
						"--profile",
						profile,
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsValidateNode,
		audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) readExpectation {
			return nodeReadExpectation(
				validatedNode,
				`{"NodeID":%q,"Class":"props_transform","Profile":""}`,
			)
		},
	)
	var previewedNode uuid.UUID
	cases["command ops repair preview"] = withReadCheck(
		commandCase(
			audit.VerbOpsRepairPreview,
			audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) commandInput {
				nodeID, profile := env.legacyRepair(t)
				previewedNode = nodeID
				return commandInput{
					args: []string{
						"ops",
						"repair",
						"preview",
						"--node",
						nodeID.String(),
						"--class",
						string(ops.RepairClassPropsTransform),
						"--profile",
						profile,
					},
					configure: nil,
					outbox:    nil,
				}
			},
		),
		audit.VerbOpsRepairPreview,
		audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) readExpectation {
			return nodeReadExpectation(
				previewedNode,
				`{"NodeID":%q,"Class":"props_transform","Profile":"","Manifest":""}`,
			)
		},
	)
	cases["command ops repair apply"] = commandCase(
		audit.VerbOpsRepairApply,
		audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			nodeID, profile := env.legacyRepair(t)
			return commandInput{
				args: []string{
					"ops",
					"repair",
					"apply",
					"--actor",
					env.server.userID.String(),
					"--yes",
					"--node",
					nodeID.String(),
					"--class",
					string(ops.RepairClassPropsTransform),
					"--profile",
					profile,
					"--confirm",
					env.repairToken(t, nodeID),
				},
				configure: nil,
				outbox:    nil,
			}
		},
	)
	cases["command ops repair reference-uniqueness"] = commandCase(
		audit.VerbOpsRepairReferenceUniqueness,
		audit.OutcomeOK,
		fixedCommand("ops", "repair", "reference-uniqueness", "--keep", "oldest"),
	)
	cases["command ops act-as create"] = actAsCreateCase()
	cases["command ops batch backfill.default_children"] = commandCase(
		audit.VerbOpsBackfillDefaultChildren,
		audit.OutcomeOK,
		fixedCommand("ops", "batch", "backfill.default_children"),
	)
	cases["command ops batch reference.duplicates"] = withReadCheck(
		commandCase(
			audit.VerbOpsReferenceDuplicates,
			audit.OutcomeCompleted,
			fixedCommand("ops", "batch", "reference.duplicates"),
		),
		audit.VerbOpsReferenceDuplicates,
		audit.OutcomeCompleted,
		fixedReadExpectation("reference/duplicates"),
	)
	cases["command ops batch reindex"] = commandCase(
		audit.VerbOpsReindex,
		audit.OutcomeOK,
		fixedCommand("ops", "batch", "reindex"),
	)
	cases["command ops verify strict-validation"] = strictValidationCase()
}

// strictValidationCase stores an issue with an undeclared property and runs
// the strict validation dry run, which must succeed and record a completed
// read. The case runs without --execute to keep strict validation inactive for
// the other cases.
func strictValidationCase() coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			nodeID, _ := env.legacyRepair(t)
			args := []string{"ops", "verify", "strict-validation"}
			operatorID, output, err := env.runCommand(
				t,
				commandInput{args: args, configure: nil, outbox: nil},
			)
			if err != nil {
				t.Fatalf(
					"The strict validation dry run failed with issue %s that stores an undeclared property: %v\n%s",
					nodeID,
					err,
					output,
				)
			}
			return operatorEvidence(operatorID)
		},
		verb:    audit.VerbOpsStrictValidation,
		outcome: audit.OutcomeCompleted,
		check:   nil,
	}
}

func actAsCreateCase() coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			input := commandInput{
				args: []string{
					"ops",
					"act-as",
					"create",
					"--user",
					env.userEmail(t),
					"--reason",
					"coverage",
					"--parent",
					env.fixture(t).nodes["project"].String(),
					"--node-type",
					"issue",
					"--name",
					"Coverage act-as " + uuid.NewString()[:8],
					"--output",
					"json",
				},
				configure: nil,
				outbox:    nil,
			}
			operatorID, output, err := env.commandWith(t, input)
			if err != nil {
				t.Fatalf("ops act-as create failed: %v\n%s", err, output)
			}
			requestID, traceID := integration.CommandCorrelation(t, output)
			isGrant := func(row audit.Row) bool {
				return row.Action == string(audit.VerbOpsActAsGrant)
			}
			rows := env.ledger.requestRows(
				t,
				env.server.orgID,
				requestID,
				func(rows []audit.Row) bool {
					return slices.ContainsFunc(rows, isGrant)
				},
			)
			grant := rows[slices.IndexFunc(rows, isGrant)]
			if grant.ActorID != operatorID || grant.Context.TraceID != traceID {
				t.Fatalf(
					"grant row %s actor %s and trace ID %q, want actor %s and trace ID %s",
					grant.EventID,
					grant.ActorID,
					grant.Context.TraceID,
					operatorID,
					traceID,
				)
			}
			return operatorEvidence(operatorID)
		},
		verb:    audit.VerbOpsActAsCreate,
		outcome: audit.OutcomeOK,
		check:   nil,
	}
}

// stores opens FoundationDB stores using the test server's cluster file.
func (env *coverageEnv) stores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(env.server.clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf(
			"FoundationDB store initialization failed; the command case requires access to the test cluster: %v",
			err,
		)
	}
	return stores
}

// legacyRepair creates an issue with a legacy property and writes a repair
// profile that removes that property. It returns the issue ID and profile path.
func (env *coverageEnv) legacyRepair(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	stores := env.stores(t)
	nodeID := env.fixture(t).create(t, "issue")
	stored, err := stores.Nodes.Get(ctx, env.server.orgID, nodeID)
	if err != nil || stored == nil {
		t.Fatalf(
			"Node lookup failed or returned nil; the repair case requires the issue node %s: %v",
			nodeID,
			err,
		)
	}
	view, err := stores.Views.Get(ctx, nodeID)
	if err != nil {
		t.Fatalf(
			"NodeListView lookup failed; the repair case requires the read row for node %s: %v",
			nodeID,
			err,
		)
	}
	stored.Props[coverageLegacyProperty] = json.RawMessage(strconv.Quote("legacy coverage text"))
	view.Props = stored.Props
	if err := stores.Nodes.Set(ctx, stored, view); err != nil {
		t.Fatalf(
			"Saving the legacy property failed; the repair case requires the property on node %s: %v",
			nodeID,
			err,
		)
	}
	profile, err := json.Marshal(env.repairProfile())
	if err != nil {
		t.Fatalf(
			"Repair profile encoding failed; the command case requires a JSON profile: %v",
			err,
		)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, profile, 0o600); err != nil {
		t.Fatalf(
			"Writing the repair profile failed; the command case requires a profile file: %v",
			err,
		)
	}
	return nodeID, path
}

// repairProfile returns a property transformation profile that removes
// coverageLegacyProperty.
func (env *coverageEnv) repairProfile() *ops.RepairReferenceProfile {
	return &ops.RepairReferenceProfile{
		Name:         "coverage",
		RemoveFields: []string{coverageLegacyProperty},
	}
}

// repairToken previews the property repair and returns its confirmation token.
// It fails the test if the preview fails or does not permit application.
func (env *coverageEnv) repairToken(t *testing.T, nodeID uuid.UUID) string {
	t.Helper()
	stores := env.stores(t)
	members := postgres.NewOrgMemberRepo(testenv.LedgerPool(t, env.server.config.DatabaseURL))
	console, err := ops.NewRepairConsole(
		stores.Nodes,
		stores.Views,
		stores.NodeTypes,
		stores.PropertyDefs,
		members,
		stores.Relationships,
		stores.Relationships,
	)
	if err != nil {
		t.Fatalf("Repair console construction failed; the repair case requires a console: %v", err)
	}
	preview, err := console.Preview(
		integration.WithTestOperation(t, t.Context()),
		ops.RepairPreviewInput{
			Class:   ops.RepairClassPropsTransform,
			NodeID:  nodeID,
			Profile: env.repairProfile(),
		},
	)
	if err != nil || !preview.CanApply {
		t.Fatalf(
			"Repair preview failed or refused application; the test requires an applicable repair for node %s: %+v %v",
			nodeID,
			preview,
			err,
		)
	}
	return preview.ConfirmationToken
}
