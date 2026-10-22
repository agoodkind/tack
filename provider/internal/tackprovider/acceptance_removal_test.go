package tackprovider_test

import (
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestSchemaDeclaredRemoval imports at TACK_ACC_VERSION, removes tack_schema.qa
// through a configuration change, and checks its planned destruction and absence
// from state. It imports again and checks that applied_version did not change.
// The test needs only a database at TACK_ACC_VERSION.
func TestSchemaDeclaredRemoval(t *testing.T) {
	environment := newAcceptanceEnvironment(t)
	version := environment.appliedVersion
	variables := environment.variables(version)
	secret := environment.secret()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(),
		Steps: []resource.TestStep{
			importStep(environment, version),
			{
				Config:          acceptanceProviderConfig,
				ConfigVariables: variables,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(schemaAddress, plancheck.ResourceActionDestroy),
					secretAbsentFromPlan{secret: secret},
				}},
				ConfigStateChecks: []statecheck.StateCheck{secretAbsentFromState{secret: secret}},
				Check: func(state *terraform.State) error {
					if _, found := state.RootModule().Resources[schemaAddress]; found {
						return errors.New("the state still contains " + schemaAddress + " after its removal")
					}
					return nil
				},
			},
			importStep(environment, version),
			{Config: acceptanceConfig, ConfigVariables: variables, PlanOnly: true},
		},
	})
}
