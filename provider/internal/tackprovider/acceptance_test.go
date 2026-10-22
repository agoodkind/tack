package tackprovider_test

import (
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestSchemaImportPlanAndRefresh imports the schema of a running Tack
// environment, plans the applied version without changes, and refreshes
// state. It does not run apply, and destroy only removes the resource from
// state.
func TestSchemaImportPlanAndRefresh(t *testing.T) {
	environment := newAcceptanceEnvironment(t)
	version := environment.appliedVersion
	versionText := strconv.FormatInt(version, 10)
	checks := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr(schemaAddress, "id", environment.importID()),
		resource.TestCheckResourceAttr(schemaAddress, "version", versionText),
		resource.TestCheckResourceAttr(schemaAddress, "applied_version", versionText),
		resource.TestCheckResourceAttr(schemaAddress, "pending.#", "0"),
		resource.TestCheckResourceAttrSet(schemaAddress, "image_commit"),
	)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(),
		Steps: []resource.TestStep{
			importStep(environment, version),
			{Config: acceptanceConfig, ConfigVariables: environment.variables(version), PlanOnly: true},
			{RefreshState: true, Check: checks},
		},
	})
}
