//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"
)

// outOfRangeStatementTimeout exceeds the largest value that the engines accept
// for statement_timeout (2147483647 milliseconds).
const outOfRangeStatementTimeout = "99999999999"

// failingCreate applies a create with a statement_timeout value that the engine
// rejects. The statement that applies statement_timeout runs after CREATE ROLE.
func (s *scenario) failingCreate(t *testing.T) {
	t.Helper()
	s.space.statementTimeout = outOfRangeStatementTimeout
	output, code := s.apply(t, "1", infinity)
	if code == exitSuccess ||
		!strings.Contains(output, "could not set statement_timeout "+outOfRangeStatementTimeout) {
		t.Fatalf(
			"the create exited %d; the test expects the statement_timeout database error:\n%s",
			code,
			output,
		)
	}
	s.space.statementTimeout = ""
}

// TestYugabyteExternalCreateThenFailure fails a create at a statement after
// CREATE ROLE. When the engine keeps the new role, state must record its ID and
// OID. A corrected apply must then converge with one role.
func TestYugabyteExternalCreateThenFailure(t *testing.T) {
	s := externalScenario(t)
	s.failingCreate(t)
	kept := s.target.roleExists(t, s.role)
	t.Logf("the engine kept the role after the failed create: %t", kept)
	if kept {
		if count := s.space.stateResourceCount(t); count != 1 {
			t.Fatalf("the role exists and state has %d resources; the test expects 1", count)
		}
		got := s.space.stateValues(t)
		if got.ID != s.role || got.OID != s.target.roleOID(t, s.role) {
			t.Fatalf(
				"state is %+v; the test expects id %s and the role OID %d",
				got,
				s.role,
				s.target.roleOID(t, s.role),
			)
		}
	} else if count := s.space.stateResourceCount(t); count != 0 {
		t.Fatalf("the role does not exist and state has %d resources; the test expects 0", count)
	}

	s.requireApplied(t, "1", infinity)
	if count := s.target.roleCount(t, s.role); count != 1 {
		t.Fatalf("the database has %d roles named %s, and the test expects 1", count, s.role)
	}
	s.requireState(t, "1", infinity)
	s.requireStateOID(t, s.target.roleOID(t, s.role))
	s.requireLogin(t, "1", true)
	s.requirePlan(t, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

// TestPostgreSQLFailedCreate fails the same create on PostgreSQL. The create
// transaction must roll back completely and leave no role and no state.
func TestPostgreSQLFailedCreate(t *testing.T) {
	s := postgresqlScenario(t)
	s.failingCreate(t)
	if s.target.roleExists(t, s.role) {
		t.Fatalf("role %s exists after the failed create", s.role)
	}
	if count := s.space.stateResourceCount(t); count != 0 {
		t.Fatalf("state has %d resources after the failed create", count)
	}
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	s.requirePlan(t, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalKeepsAdvisoryEraMemberships creates a role in advisory
// mode with a configured membership, then switches the provider to external mode
// and omits roles. The plan must not fail on the populated state, the apply must
// keep the membership in the database, and the repeat plan must be empty.
func TestYugabyteExternalKeepsAdvisoryEraMemberships(t *testing.T) {
	s := externalScenario(t)
	sentinel := randomText(t, "sentinel_")
	t.Cleanup(func() { s.target.dropRole(t, sentinel) })
	s.target.createRole(t, sentinel, "")

	s.space.lockMode = ""
	s.space.memberships = `["` + sentinel + `"]`
	s.requireApplied(t, "1", infinity)
	if !s.target.isMember(t, s.role, sentinel) {
		t.Fatalf("role %s is not a member of %s after the advisory-mode create", s.role, sentinel)
	}

	s.space.lockMode = externalMode
	s.space.memberships = ""
	output, code := s.plan(t, "1", infinity)
	if code == exitFailure {
		t.Fatalf("the external-mode plan rejected the populated state:\n%s", output)
	}
	s.requireApplied(t, "1", infinity)
	if !s.target.isMember(t, s.role, sentinel) {
		t.Fatalf("the external-mode apply removed the membership of %s in %s", s.role, sentinel)
	}
	s.requirePlan(t, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalRejectsEmptyRoles configures roles as an empty set. The
// attribute still claims ownership of memberships.
func TestYugabyteExternalRejectsEmptyRoles(t *testing.T) {
	s := externalScenario(t)
	s.space.memberships = `[]`
	output, code := s.plan(t, "1", infinity)
	if code != exitFailure || !strings.Contains(output, "roles is not supported") {
		t.Fatalf("plan exited %d and the test expects the roles diagnostic:\n%s", code, output)
	}
}

func TestYugabyteExternalDeleteKeepsRestrictedAdminMemberships(t *testing.T) {
	s := externalScenario(t)
	administrator := randomText(t, "limited_")
	password := randomText(t, "limited_password_")
	s.target.createRole(t, administrator, password)
	s.target.createRole(t, s.role, s.password("1"))
	s.target.grantMembership(t, administrator, s.role)
	t.Cleanup(func() {
		s.target.dropRole(t, s.role)
		s.target.dropRole(t, administrator)
	})
	if !s.target.isMember(t, s.role, administrator) || s.target.isMember(t, administrator, s.role) {
		t.Fatal("the test memberships do not match the restricted administrator setup")
	}

	s.space.target.adminUser = administrator
	s.space.target.adminPassword = password
	variables := s.space.roleVariables(s.role, s.password("1"), "1", infinity)
	output, code := s.space.run(
		t,
		variables,
		"import",
		"-no-color",
		"-input=false",
		"postgresql_role.app",
		s.role,
	)
	if code != exitSuccess {
		t.Fatalf("import as the restricted administrator exited %d:\n%s", code, output)
	}
	output, code = s.space.run(
		t,
		variables,
		"destroy",
		"-auto-approve",
		"-no-color",
		"-input=false",
	)
	if code == exitSuccess || !strings.Contains(output, "could not reassign owned") {
		t.Fatalf(
			"destroy exited %d; the test expects an ownership reassignment error:\n%s",
			code,
			output,
		)
	}
	if !s.target.isMember(t, s.role, administrator) || s.target.isMember(t, administrator, s.role) {
		t.Fatal("the failed delete changed the administrator memberships")
	}
	if !s.target.roleExists(t, s.role) {
		t.Fatal("the failed delete removed the managed role")
	}
	s.space.requireNoSecrets(t, append(s.secrets(), password)...)
}
