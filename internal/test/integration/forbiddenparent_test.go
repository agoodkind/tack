package integration_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

func TestMCPRejectsParentProperty(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	issueID, _ := createSchemaIssue(t, harness, "Issue with relationship parent")
	before := storedStoreView(t, stores, issueID)
	parentID := storedParentTarget(t, stores, before)
	beforeCount := len(scopeIssues(t, stores, before))

	for _, value := range []json.RawMessage{quoted(parentID), json.RawMessage("null")} {
		create := schemaArguments(harness)
		create.Name = "Issue with rejected parent property"
		create.Properties = datagen.NodeProperties{"parent_id": value}
		if refusal := harness.CallExpectError(t, "tack_create_issue", create); !strings.Contains(
			refusal,
			"properties.parent_id is not declared",
		) {
			t.Fatalf(
				"create with parent_id %s returned %q, want undeclared-property rejection",
				value,
				refusal,
			)
		}

		if count := len(scopeIssues(t, stores, before)); count != beforeCount {
			t.Fatalf("rejected create changed issue count from %d to %d", beforeCount, count)
		}

		update := schemaArguments(harness)
		update.ProjectReference = ""
		update.NodeID = issueID.String()
		update.Properties = datagen.NodeProperties{"parent_id": value}
		if refusal := harness.CallExpectError(t, "tack_update_issue", update); !strings.Contains(
			refusal,
			"properties.parent_id is not declared",
		) {
			t.Fatalf(
				"update with parent_id %s returned %q, want undeclared-property rejection",
				value,
				refusal,
			)
		}

		after := storedStoreView(t, stores, issueID)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf(
				"rejected update changed issue %s: before %+v, after %+v",
				issueID,
				before,
				after,
			)
		}

		requireParents(t, stores, issueID.String(), []string{parentID})
	}
}
