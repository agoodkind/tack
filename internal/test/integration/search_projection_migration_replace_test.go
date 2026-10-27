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
// definition correction. The test stores parent_id with search inclusion,
// then indexes a parent node and a child node with a parent_id value.
// The test calls ops.RunFixedDefinitionCorrection once, which corrects
// exactly one definition through one PropertyDefStore.Replace call.
// Before any worker runs after the correction, the test requires a fresh
// reader page of the parent node to have a projection version that differs
// from the indexed page. It requires exactly one property_def.update event
// with a delta for the definition in the audit outbox. It also requires a
// claimable rescan for the organization.
// After the worker runs, the test requires the child pages to omit the
// parent UUID.
func TestSearchProjectionMigrationReplaceCommitsDigestAuditAndRescan(t *testing.T) {
	env := newMigrationEnv(t, 256)
	parentDefinition := env.putStoredBuiltIn(t, "parent_id", true, nil)
	parent := env.putNodes(t, 1, "copper meadow lantern")[0]
	child := uuid.Must(uuid.NewV7())
	env.putNodeProps(t, child, map[string]json.RawMessage{env.IncludedKey: encodeText(t, "zephyr orchard beacon"), "parent_id": encodeText(t, parent.String())})
	env.runIdle(t)
	indexed := env.pages(t, parent, false)[0].ProjectionVersion

	principal := audit.OperatorPrincipal{ID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca32"), Email: "operator@example.invalid", Name: "Operator", Source: "test"}
	result, err := ops.RunFixedDefinitionCorrection(audit.WithOperatorPrincipal(integration.WithTestOperation(t, t.Context()), principal), env.Stores.PropertyDefs, false)
	if err != nil || result.Corrected != 1 {
		t.Fatalf("correction = %+v, err %v; want one corrected definition", result, err)
	}
	if fresh := env.readerPages(t, parent)[0].ProjectionVersion; fresh == indexed {
		t.Fatalf("reader projection after the correction = %q, want a version other than the indexed %q", fresh, indexed)
	}
	env.requireCorrectionEvents(t, []uuid.UUID{parentDefinition.ID})
	store := env.Stores.SearchWork(clock.Wall{})
	rescan, err := store.Claim(integration.WithTestOperation(t, t.Context()), searchdomain.WorkClassRescan, "replace-check", time.Minute)
	if err != nil || rescan.OrgID != env.OrgID {
		t.Fatalf("rescan after the correction = %+v, err %v; want the organization's rescan", rescan, err)
	}
	if err := store.Yield(integration.WithTestOperation(t, t.Context()), rescan); err != nil {
		t.Fatalf("yield the rescan: %v", err)
	}
	env.runIdle(t)
	if text := env.pageText(t, child); strings.Contains(text, parent.String()) {
		t.Fatalf("child pages after the correction = %q, want them without the parent UUID", text)
	}
}
