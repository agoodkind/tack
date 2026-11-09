//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"
)

// aliasConfigurationText declares two provider instances with one connection
// string. The default instance uses external lock mode, and the advisory alias
// uses the provider default. Each instance manages one role.
const aliasConfigurationText = `
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
  role_lock_mode   = "external"
  host             = var.host
  port             = var.port
  database         = var.database
  username         = var.admin_user
  password         = var.admin_password
  sslmode          = "disable"
}

provider "postgresql" {
  alias            = "advisory"
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

resource "postgresql_role" "advisory" {
  provider            = postgresql.advisory
  name                = "${var.role_name}_adv"
  login               = true
  password_wo         = var.role_password
  password_wo_version = var.role_password_version
  valid_until         = var.valid_until
}
`

// TestYugabyteLockModesStayPerProvider updates two roles with provider
// instances that share a connection string and use different role_lock_mode
// values. The external instance must update its role. The advisory instance
// must fail to acquire an advisory lock.
func TestYugabyteLockModesStayPerProvider(t *testing.T) {
	s := externalScenario(t)
	advisoryRole := s.role + "_adv"
	t.Cleanup(func() { s.target.dropRole(t, advisoryRole) })
	s.space.host.WriteFile(t, s.space.host.Directory+"/main.tf", []byte(aliasConfigurationText))

	s.requireApplied(t, "1", infinity)
	output, code := s.apply(t, "2", infinity)
	if code == exitSuccess || !strings.Contains(strings.ToLower(output), "advisory") {
		t.Fatalf("apply exited %d and the test expects the advisory lock error:\n%s", code, output)
	}
	s.requireLogin(t, "2", true)
	s.requireLogin(t, "1", false)
	if err := s.target.loginError(t, advisoryRole, s.password("1")); err != nil {
		t.Fatalf("the advisory role lost its first password: %v", err)
	}
	if err := s.target.loginError(t, advisoryRole, s.password("2")); err == nil {
		t.Fatalf("the advisory role accepts the second password, and its update must fail")
	}
	s.requireNoSecrets(t)
}
