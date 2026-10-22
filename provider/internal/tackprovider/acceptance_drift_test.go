package tackprovider_test

import (
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestSchemaDriftRepair imports and adopts at TACK_ACC_VERSION without a resource
// change, then plans the higher TACK_ACC_TARGET_VERSION without applying it. The
// apply plan must still report the starting applied_version; apply must set it
// to the target without changing image_commit. The next plans must be empty,
// including the plan before a final apply.
func TestSchemaDriftRepair(t *testing.T) {
	environment := newAcceptanceEnvironment(t)
	applied := environment.appliedVersion
	target := requireNumber(t, targetVersionVariable)
	if target <= applied {
		t.Fatalf("%s is %d and must exceed %s, which is %d", targetVersionVariable, target, versionVariable, applied)
	}
	secret := environment.secret()
	sameImage := statecheck.CompareValue(compare.ValuesSame())
	imagePath := tfjsonpath.New("image_commit")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(),
		Steps: []resource.TestStep{
			importStep(environment, applied),
			adoptStep(environment, applied, sameImage.AddStateValue(schemaAddress, imagePath)),
			{
				Config:             outputConfig,
				ConfigVariables:    environment.variables(target),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(schemaAddress, plancheck.ResourceActionUpdate),
					secretAbsentFromPlan{secret: secret},
				}},
			},
			migrateStep(environment, applied, target, sameImage.AddStateValue(schemaAddress, imagePath)),
			repeatStep(environment, target),
		},
	})
}

func adoptStep(
	environment acceptanceEnvironment,
	version int64,
	extraChecks ...statecheck.StateCheck,
) resource.TestStep {
	secret := environment.secret()
	return resource.TestStep{
		Config:          outputConfig,
		ConfigVariables: environment.variables(version),
		ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction(schemaAddress, plancheck.ResourceActionNoop),
			secretAbsentFromPlan{secret: secret},
		}},
		ConfigStateChecks: append([]statecheck.StateCheck{secretAbsentFromState{secret: secret}}, extraChecks...),
		Check:             appliedChecks(version),
	}
}

func migrateStep(
	environment acceptanceEnvironment,
	prior int64,
	target int64,
	extraChecks ...statecheck.StateCheck,
) resource.TestStep {
	secret := environment.secret()
	return resource.TestStep{
		Config:          outputConfig,
		ConfigVariables: environment.variables(target),
		ConfigPlanChecks: resource.ConfigPlanChecks{
			PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(schemaAddress, plancheck.ResourceActionUpdate),
				priorAppliedVersion{version: prior},
				secretAbsentFromPlan{secret: secret},
			},
			PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
		},
		ConfigStateChecks: append([]statecheck.StateCheck{secretAbsentFromState{secret: secret}}, extraChecks...),
		Check:             appliedChecks(target),
	}
}

// repeatStep returns a step that applies the same configuration again and
// requires an empty plan before apply. OpenTofu does not call the provider to
// change a resource when the plan is empty.
func repeatStep(environment acceptanceEnvironment, version int64) resource.TestStep {
	return resource.TestStep{
		Config:          outputConfig,
		ConfigVariables: environment.variables(version),
		ConfigPlanChecks: resource.ConfigPlanChecks{
			PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
		},
		Check: appliedChecks(version),
	}
}

func appliedChecks(version int64) resource.TestCheckFunc {
	versionText := strconv.FormatInt(version, 10)
	return resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr(schemaAddress, "version", versionText),
		resource.TestCheckResourceAttr(schemaAddress, "applied_version", versionText),
		resource.TestCheckResourceAttr(schemaAddress, "pending.#", "0"),
	)
}
