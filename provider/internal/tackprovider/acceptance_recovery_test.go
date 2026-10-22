package tackprovider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestSchemaInterruptedRecovery starts at TACK_ACC_VERSION with
// TACK_ACC_TARGET_VERSION at least two versions higher. A separate OpenTofu state
// applies one migration while this test's state still records the starting
// version, as after an interrupted apply. Recovery must refresh applied_version,
// finish the remaining migrations with --expect-from at that version, and
// require an empty plan before a final apply.
func TestSchemaInterruptedRecovery(t *testing.T) {
	environment := newAcceptanceEnvironment(t)
	applied := environment.appliedVersion
	target := requireNumber(t, targetVersionVariable)
	if target < applied+2 {
		t.Fatalf("%s is %d and must be at least two above %s, which is %d",
			targetVersionVariable, target, versionVariable, applied)
	}
	completed := applied + 1
	recovery := migrateStep(environment, completed, target)
	recovery.PreConfig = func() { migrateInSeparateState(t, environment, applied, completed) }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(),
		Steps: []resource.TestStep{
			importStep(environment, applied),
			adoptStep(environment, applied),
			recovery,
			repeatStep(environment, target),
		},
	})
}

// migrateInSeparateState imports the schema at from into a new working directory,
// applies to, and destroys that state. Destroy removes only the resource from
// that state and leaves the database at to.
func migrateInSeparateState(t *testing.T, environment acceptanceEnvironment, from int64, to int64) {
	t.Helper()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(),
		Steps: []resource.TestStep{
			importStep(environment, from),
			{Config: acceptanceConfig, ConfigVariables: environment.variables(to), Check: appliedChecks(to)},
		},
	})
}
