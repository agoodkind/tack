package tackprovider_test

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"goodkind.io/tack/provider/internal/tackprovider"
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
	targetVersionVariable = "TACK_ACC_TARGET_VERSION"
	operatorEmailVariable = "TACK_ACC_OPERATOR_EMAIL"

	schemaAddress = "tack_schema.qa"
)

// acceptanceProviderConfig declares the OpenTofu variables and the tack provider
// block, with every value supplied through a variable. The api_token variable is
// sensitive. The configuration text does not contain the token value.
const acceptanceProviderConfig = `
terraform {
  required_providers {
    tack = { source = "tofu.home.arpa/agoodkind/tack" }
  }
}

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
`

const (
	acceptanceConfig = acceptanceProviderConfig + `
resource "tack_schema" "qa" {
  node        = var.node
  vmid        = var.vmid
  install_dir = var.install_dir
  version     = var.schema_version
}
`

	// outputConfig adds the whole tack_schema.qa object as an output to
	// acceptanceConfig. State checks inspect both resources and outputs.
	outputConfig = acceptanceConfig + `
output "schema" { value = tack_schema.qa }
`
)

type acceptanceEnvironment struct {
	node           string
	endpoint       string
	nodeName       string
	token          string
	vmid           int64
	installDir     string
	appliedVersion int64
	operatorEmail  string
}

func newAcceptanceEnvironment(t *testing.T) acceptanceEnvironment {
	t.Helper()
	if os.Getenv(acceptanceVariable) == "" {
		t.Skipf("%s is not set", acceptanceVariable)
	}
	tokenBytes, err := os.ReadFile(requireVariable(t, tokenFileVariable))
	if err != nil {
		t.Fatalf("read the token file named by %s: %v", tokenFileVariable, err)
	}
	return acceptanceEnvironment{
		node:           requireVariable(t, nodeVariable),
		endpoint:       requireVariable(t, endpointVariable),
		nodeName:       requireVariable(t, nodeNameVariable),
		token:          strings.TrimSpace(string(tokenBytes)),
		vmid:           requireNumber(t, vmidVariable),
		installDir:     requireVariable(t, installDirVariable),
		appliedVersion: requireNumber(t, versionVariable),
		operatorEmail:  requireVariable(t, operatorEmailVariable),
	}
}

func (environment acceptanceEnvironment) importID() string {
	return fmt.Sprintf("%s/%d:%s", environment.node, environment.vmid, environment.installDir)
}

// secret returns the part of the acceptanceEnvironment API token after the first
// =. Credential checks search for this value.
func (environment acceptanceEnvironment) secret() string {
	_, secret, _ := strings.Cut(environment.token, "=")
	return secret
}

func (environment acceptanceEnvironment) variables(version int64) config.Variables {
	return config.Variables{
		"node":           config.StringVariable(environment.node),
		"endpoint":       config.StringVariable(environment.endpoint),
		"node_name":      config.StringVariable(environment.nodeName),
		"api_token":      config.StringVariable(environment.token),
		"vmid":           config.IntegerVariable(environment.vmid),
		"install_dir":    config.StringVariable(environment.installDir),
		"schema_version": config.IntegerVariable(version),
		"operator_email": config.StringVariable(environment.operatorEmail),
	}
}

func importStep(environment acceptanceEnvironment, version int64) resource.TestStep {
	versionText := strconv.FormatInt(version, 10)
	return resource.TestStep{
		Config:             acceptanceConfig,
		ConfigVariables:    environment.variables(version),
		ResourceName:       schemaAddress,
		ImportState:        true,
		ImportStateId:      environment.importID(),
		ImportStatePersist: true,
		ImportStateCheck: func(states []*terraform.InstanceState) error {
			if len(states) != 1 || states[0].Attributes["applied_version"] != versionText {
				return errors.New("the import did not return one state at the applied version " + versionText)
			}
			return nil
		},
	}
}

func providerFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"tack": providerserver.NewProtocol6WithError(tackprovider.New("acceptance")()),
	}
}

func requireVariable(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must be set for the acceptance test", name)
	}
	return value
}

func requireNumber(t *testing.T, name string) int64 {
	t.Helper()
	number, err := strconv.ParseInt(requireVariable(t, name), 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return number
}
