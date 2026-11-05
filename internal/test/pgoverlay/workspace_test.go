//go:build pgoverlay

package pgoverlay_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

const (
	providerSource = "tack.invalid/overlay/postgresql"

	// providerDirectory is where Dockerfile.pgoverlay-test installs the built
	// provider in the runner container.
	providerDirectory = "/opt/overlay"

	// configurationText declares one login role with a write-only password and
	// a password version. Every credential arrives through a sensitive and
	// ephemeral variable.
	configurationText = `
terraform {
  required_providers {
    postgresql = { source = "` + providerSource + `" }
  }
}

variable "host" { type = string }
variable "port" { type = number }
variable "database" { type = string }
variable "admin_user" { type = string }
variable "admin_password" {
  type      = string
  sensitive = true
  ephemeral = true
}
variable "role_name" { type = string }
variable "role_password" {
  type      = string
  sensitive = true
  ephemeral = true
}
variable "role_password_version" { type = string }
variable "valid_until" { type = string }
variable "expected_version" { type = string }

provider "postgresql" {
  expected_version = var.expected_version
  host             = var.host
  port             = var.port
  database         = var.database
  username         = var.admin_user
  password         = var.admin_password
  sslmode          = "disable"
}

resource "postgresql_role" "app" {
  name                = var.role_name
  login               = true
  password_wo         = var.role_password
  password_wo_version = var.role_password_version
  valid_until         = var.valid_until
}
`

	cliConfigFormat = `provider_installation {
  dev_overrides {
    "%s" = "%s"
  }
  direct {}
}
`
)

// workspace is one OpenTofu working directory inside the runner container. The
// runner image contains the built provider and the pinned OpenTofu.
type workspace struct {
	host       testenv.OpenTofuHost
	target     databaseTarget
	transcript strings.Builder
}

func newWorkspace(t *testing.T, target databaseTarget) *workspace {
	t.Helper()
	host := testenv.StartOpenTofuHost(t)
	space := &workspace{host: host, target: target}
	host.WriteFile(t, space.cliConfigPath(), fmt.Appendf(nil, cliConfigFormat, providerSource, providerDirectory))
	host.WriteFile(t, host.Directory+"/main.tf", []byte(configurationText))
	space.requireVersion(t)
	return space
}

func (space *workspace) cliConfigPath() string {
	return space.host.Directory + "/tofu.rc"
}

func (space *workspace) stateFilePath() string {
	return space.host.Directory + "/terraform.tfstate"
}

func (space *workspace) requireVersion(t *testing.T) {
	t.Helper()
	output, code := space.run(t, nil, "version", "-json")
	if code != 0 {
		t.Fatalf("tofu version exited %d", code)
	}
	var document struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("read the tofu version: %v", err)
	}
	want := requireVariable(t, tofuVersionVariable)
	if document.Version != want {
		t.Fatalf("tofu reports version %s, and the pinned version is %s", document.Version, want)
	}
}

func (space *workspace) run(t *testing.T, variables map[string]string, args ...string) (string, int) {
	t.Helper()
	environment := []string{
		"HOME=" + space.host.Directory,
		"TF_CLI_CONFIG_FILE=" + space.cliConfigPath(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	}
	for name, value := range variables {
		environment = append(environment, "TF_VAR_"+name+"="+value)
	}
	command := append([]string{"tofu", "-chdir=" + space.host.Directory}, args...)
	output, code := space.host.Run(t, environment, command...)
	space.transcript.WriteString(output)
	return output, code
}

func (space *workspace) roleVariables(role, password, version, validUntil string) map[string]string {
	return map[string]string{
		"host":                  space.target.host,
		"port":                  space.target.port,
		"database":              space.target.database,
		"admin_user":            space.target.adminUser,
		"admin_password":        space.target.adminPassword,
		"role_name":             role,
		"role_password":         password,
		"role_password_version": version,
		"valid_until":           validUntil,
		"expected_version":      space.target.expectedVersion,
	}
}
