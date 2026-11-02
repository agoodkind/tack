//go:build pgoverlay

package pgoverlay_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	providerSource = "tack.invalid/overlay/postgresql"
	fileMode       = 0o600

	// configurationText declares one login role with a write-only password and
	// a password version. Every credential arrives through a sensitive variable.
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
  host     = var.host
  port     = var.port
  database = var.database
  username = var.admin_user
  password = var.admin_password
  sslmode  = "disable"
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

// workspace is one OpenTofu working directory that uses the built provider.
type workspace struct {
	directory  string
	cliConfig  string
	tofu       string
	target     databaseTarget
	transcript bytes.Buffer
}

func newWorkspace(t *testing.T, target databaseTarget) *workspace {
	t.Helper()
	provider := requireVariable(t, providerVariable)
	tofu := requireVariable(t, tofuVariable)
	directory := t.TempDir()
	cliConfig := filepath.Join(directory, "tofu.rc")
	text := fmt.Sprintf(cliConfigFormat, providerSource, filepath.Dir(provider))
	if err := os.WriteFile(cliConfig, []byte(text), fileMode); err != nil {
		t.Fatalf("write the OpenTofu CLI configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.tf"), []byte(configurationText), fileMode); err != nil {
		t.Fatalf("write the OpenTofu configuration: %v", err)
	}
	space := &workspace{directory: directory, cliConfig: cliConfig, tofu: tofu, target: target}
	space.requireVersion(t)
	return space
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

// run starts tofu in the workspace and returns its output and exit code.
func (space *workspace) run(t *testing.T, variables map[string]string, args ...string) (string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), space.tofu, append([]string{"-chdir=" + space.directory}, args...)...)
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + space.directory,
		"TF_CLI_CONFIG_FILE=" + space.cliConfig,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	}
	for name, value := range variables {
		command.Env = append(command.Env, "TF_VAR_"+name+"="+value)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	space.transcript.Write(output.Bytes())
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return output.String(), exitError.ExitCode()
	}
	if err != nil {
		t.Fatalf("start tofu: %v", err)
	}
	return output.String(), 0
}

// roleVariables returns the variable values for one role configuration.
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

// roleState is the role data that tofu records in state.
type roleState struct {
	ID         string
	Name       string
	Version    string
	ValidUntil string
}

// stateValues returns the role attributes that tofu records in state.
func (space *workspace) stateValues(t *testing.T) roleState {
	t.Helper()
	output, code := space.run(t, nil, "show", "-json")
	if code != 0 {
		t.Fatalf("tofu show exited %d", code)
	}
	var document struct {
		Values struct {
			RootModule struct {
				Resources []struct {
					Values struct {
						ID         string `json:"id"`
						Name       string `json:"name"`
						Version    string `json:"password_wo_version"`
						ValidUntil string `json:"valid_until"`
					} `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("read the state document: %v", err)
	}
	resources := document.Values.RootModule.Resources
	if len(resources) != 1 {
		t.Fatalf("state has %d resources, and the test expects 1", len(resources))
	}
	values := resources[0].Values
	return roleState{ID: values.ID, Name: values.Name, Version: values.Version, ValidUntil: values.ValidUntil}
}

// requireNoSecrets fails when a secret appears in any command output or in a
// file under the workspace.
func (space *workspace) requireNoSecrets(t *testing.T, secrets ...string) {
	t.Helper()
	found := []string{space.transcript.String()}
	walkErr := filepath.WalkDir(space.directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found = append(found, string(contents))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("read the workspace files: %v", walkErr)
	}
	for _, text := range found {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("a generated credential appears in a command output or workspace file")
			}
		}
	}
}
