package tackprovider_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"goodkind.io/tack/internal/tackprovider"
)

const (
	acceptanceVariable    = "TF_ACC"
	nodeVariable          = "TACK_ACC_NODE"
	endpointVariable      = "TACK_ACC_ENDPOINT"
	nodeNameVariable      = "TACK_ACC_NODE_NAME"
	tokenFileVariable     = "TACK_ACC_TOKEN_FILE"
	vmidVariable          = "TACK_ACC_VMID"
	installDirVariable    = "TACK_ACC_INSTALL_DIR"
	versionVariable       = "TACK_ACC_VERSION"
	operatorEmailVariable = "TACK_ACC_OPERATOR_EMAIL"

	schemaAddress = "tack_schema.qa"
)

// acceptanceConfig reads every input from OpenTofu variables. It references the
// sensitive api_token variable. The configuration text does not contain the
// token value.
const acceptanceConfig = `
variable "node" { type = string }
variable "endpoint" { type = string }
variable "node_name" { type = string }
variable "api_token" {
  type      = string
  sensitive = true
}
variable "vmid" { type = number }
variable "install_dir" { type = string }
variable "schema_version" { type = number }
variable "operator_email" { type = string }

provider "tack" {
  nodes = {
    (var.node) = {
      endpoint  = var.endpoint
      api_token = var.api_token
      insecure  = true
      node_name = var.node_name
    }
  }
  operator {
    email = var.operator_email
    name  = "tack provider acceptance test"
  }
}

resource "tack_schema" "qa" {
  node        = var.node
  vmid        = var.vmid
  install_dir = var.install_dir
  version     = var.schema_version
}
`

// TestSchemaImportPlanAndRefresh imports the schema of a running Tack
// environment, plans the applied version without changes, and refreshes
// state. It does not run apply, and destroy only removes the resource from
// state.
func TestSchemaImportPlanAndRefresh(t *testing.T) {
	if os.Getenv(acceptanceVariable) == "" {
		t.Skipf("%s is not set", acceptanceVariable)
	}
	tokenBytes, err := os.ReadFile(requireVariable(t, tokenFileVariable))
	if err != nil {
		t.Fatalf("read the token file named by %s: %v", tokenFileVariable, err)
	}
	node := requireVariable(t, nodeVariable)
	vmid := requireVariable(t, vmidVariable)
	installDir := requireVariable(t, installDirVariable)
	version := requireVariable(t, versionVariable)
	vmidNumber, err := strconv.Atoi(vmid)
	if err != nil {
		t.Fatalf("%s: %v", vmidVariable, err)
	}
	versionNumber, err := strconv.Atoi(version)
	if err != nil {
		t.Fatalf("%s: %v", versionVariable, err)
	}
	importID := node + "/" + vmid + ":" + installDir
	variables := config.Variables{
		"node":           config.StringVariable(node),
		"endpoint":       config.StringVariable(requireVariable(t, endpointVariable)),
		"node_name":      config.StringVariable(requireVariable(t, nodeNameVariable)),
		"api_token":      config.StringVariable(strings.TrimSpace(string(tokenBytes))),
		"vmid":           config.IntegerVariable(vmidNumber),
		"install_dir":    config.StringVariable(installDir),
		"schema_version": config.IntegerVariable(versionNumber),
		"operator_email": config.StringVariable(requireVariable(t, operatorEmailVariable)),
	}
	checks := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr(schemaAddress, "id", importID),
		resource.TestCheckResourceAttr(schemaAddress, "version", version),
		resource.TestCheckResourceAttr(schemaAddress, "applied_version", version),
		resource.TestCheckResourceAttr(schemaAddress, "pending.#", "0"),
		resource.TestCheckResourceAttrSet(schemaAddress, "image_commit"),
	)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"tack": providerserver.NewProtocol6WithError(tackprovider.New("acceptance")()),
		},
		Steps: []resource.TestStep{
			{
				Config:             acceptanceConfig,
				ConfigVariables:    variables,
				ResourceName:       schemaAddress,
				ImportState:        true,
				ImportStateId:      importID,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["applied_version"] != version {
						return errors.New("the import did not return one state at the applied version " + version)
					}
					return nil
				},
			},
			{Config: acceptanceConfig, ConfigVariables: variables, PlanOnly: true},
			{Config: acceptanceConfig, ConfigVariables: variables, RefreshState: true, Check: checks},
		},
	})
}

func requireVariable(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must be set for the acceptance test", name)
	}
	return value
}
