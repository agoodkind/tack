package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchProjectionMigrationReplaceCommitsDigestAuditAndRescan checks one
// definition correction. The test stores scope_id with search inclusion,
// then indexes a scope node and a node with that scope_id value.
// The test calls ops.RunFixedDefinitionCorrection once, which corrects
// exactly one definition through one PropertyDefStore.Replace call.
// Before any worker runs after the correction, the test requires a fresh
// reader page of the scope node to have a projection version that differs
// from the indexed page. It requires exactly one property_def.update event
// with a delta for the definition in the audit outbox. It also requires a
// claimable rescan for the organization.
// After the worker runs, the test requires the node's pages to omit the
// scope UUID.
func TestSearchProjectionMigrationReplaceCommitsDigestAuditAndRescan(t *testing.T) {
	env := newMigrationEnv(t, 256)
	scopeDefinition := env.putStoredBuiltIn(t, "scope_id", true, nil)
	scope := env.putNodes(t, 1, "copper meadow lantern")[0]
	child := uuid.Must(uuid.NewV7())
	env.putNodeProps(
		t,
		child,
		map[string]json.RawMessage{
			env.IncludedKey: encodeText(t, "zephyr orchard beacon"),
			"scope_id":      encodeText(t, scope.String()),
		},
	)
	env.runIdle(t)
	indexed := env.pages(t, scope, false)[0].ProjectionVersion

	principal := audit.OperatorPrincipal{
		ID:     uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca32"),
		Email:  "operator@example.invalid",
		Name:   "Operator",
		Source: "test",
	}
	result, err := ops.RunFixedDefinitionCorrection(
		audit.WithOperatorPrincipal(integration.WithTestOperation(t, t.Context()), principal),
		env.Stores.PropertyDefs,
		false,
	)
	if err != nil || result.Corrected != 1 {
		t.Fatalf("correction = %+v, err %v; want one corrected definition", result, err)
	}

	if fresh := env.readerPages(t, scope)[0].ProjectionVersion; fresh == indexed {
		t.Fatalf(
			"reader projection after the correction = %q, want a version other than the indexed %q",
			fresh,
			indexed,
		)
	}

	env.requireCorrectionEvents(t, []uuid.UUID{scopeDefinition.ID})
	store := env.Stores.SearchWork(clock.Wall{})
	rescan, claimed, err := store.Claim(
		integration.WithTestOperation(t, t.Context()),
		searchdomain.WorkClassRescan,
		"replace-check",
		time.Minute,
	)
	if err != nil || !claimed || rescan.OrgID != env.OrgID {
		t.Fatalf(
			"rescan after the correction = %+v, claimed %t, err %v; want a non-empty claim of the organization's rescan",
			rescan,
			claimed,
			err,
		)
	}

	if err := store.Yield(integration.WithTestOperation(t, t.Context()), rescan); err != nil {
		t.Fatalf("yield the rescan: %v", err)
	}

	env.runIdle(t)
	if text := env.pageText(t, child); strings.Contains(text, scope.String()) {
		t.Fatalf("node pages after the correction = %q, want them without the scope UUID", text)
	}
}
