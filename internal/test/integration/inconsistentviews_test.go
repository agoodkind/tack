package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestParentRelationshipMigrationRefusesOrphanLegacyView(t *testing.T) {
	for _, fixture := range []struct {
		name             string
		removeResolution bool
	}{
		{name: "with-resolution", removeResolution: false},
		{name: "without-resolution", removeResolution: true},
	} {
		t.Run(
			fixture.name,
			func(t *testing.T) {
				env, project := parentMigrationRefusalScope(t)
				issue := createRepairIssue(t, env, project, "Orphan legacy parent view")
				storeLegacyParentFixture(t, env, issue, project, true)
				removeMigrationPrimaryFixture(t, env, issue)
				if fixture.removeResolution {
					removeParentResolutionFixture(t, issue)
				}

				requireParentMigrationRefused(t, []uuid.UUID{issue}, "matching primary")
			},
		)
	}
}

func TestParentRelationshipMigrationRefusesViewOnlyParentWithEdge(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "View-only parent with relationship")
	storeLegacyParentFixture(t, env, issue, project, false)
	removePrimaryParentFixture(t, env, issue)
	requireParentMigrationRefused(t, []uuid.UUID{issue}, "only the read view")
}

func TestParentRelationshipMigrationRefusesInvalidScope(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		value json.RawMessage
	}{
		{name: "malformed", value: json.RawMessage(`"not-a-uuid"`)},
		{name: "zero", value: json.RawMessage(`"00000000-0000-0000-0000-000000000000"`)},
	} {
		t.Run(
			fixture.name,
			func(t *testing.T) {
				env, project := parentMigrationRefusalScope(t)
				issue := createRepairIssue(t, env, project, "Legacy parent with invalid scope")
				storeLegacyParentFixture(t, env, issue, project, true)
				storeMigrationScopeFixture(t, env, issue, fixture.value)
				requireParentMigrationRefused(t, []uuid.UUID{issue}, "scope_id")
			},
		)
	}
}
