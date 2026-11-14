//go:build pgoverlay

package pgoverlay_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	randomByteCount  = 12
	infinity         = "infinity"
	invalidValidDate = "not a timestamp"

	exitSuccess = 0
	exitChanges = 2

	savedPlanName = "saved.tfplan"
)

// scenario is one role with two generated passwords on one engine.
type scenario struct {
	space          *workspace
	target         databaseTarget
	role           string
	firstPassword  string
	secondPassword string
}

func randomText(t *testing.T, prefix string) string {
	t.Helper()
	buffer := make([]byte, randomByteCount)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return prefix + hex.EncodeToString(buffer)
}

func newScenario(t *testing.T, target databaseTarget) *scenario {
	t.Helper()
	role := randomText(t, "overlay_")
	t.Cleanup(func() { target.dropRole(t, role) })
	return &scenario{
		space:          newWorkspace(t, target),
		target:         target,
		role:           role,
		firstPassword:  randomText(t, "first-"),
		secondPassword: randomText(t, "second-"),
	}
}

func (s *scenario) password(version string) string {
	if version == "1" {
		return s.firstPassword
	}
	return s.secondPassword
}

func (s *scenario) apply(t *testing.T, version, validUntil string) (string, int) {
	t.Helper()
	return s.applyRole(t, s.role, version, validUntil)
}

// applyRole runs tofu apply with a role name that can differ from the name in state.
func (s *scenario) applyRole(t *testing.T, role, version, validUntil string) (string, int) {
	t.Helper()
	variables := s.space.roleVariables(role, s.password(version), version, validUntil)
	return s.space.run(t, variables, "apply", "-auto-approve", "-no-color", "-input=false")
}

// plan runs tofu plan with a detailed exit code, which is 2 when changes exist.
func (s *scenario) plan(t *testing.T, version, validUntil string) (string, int) {
	t.Helper()
	variables := s.space.roleVariables(s.role, s.password(version), version, validUntil)
	return s.space.run(t, variables, "plan", "-detailed-exitcode", "-no-color", "-input=false")
}

func (s *scenario) requireApplied(t *testing.T, version, validUntil string) {
	t.Helper()
	output, code := s.apply(t, version, validUntil)
	if code != exitSuccess {
		t.Fatalf("apply of password version %s exited %d:\n%s", version, code, output)
	}
}

func (s *scenario) requirePlan(t *testing.T, version, validUntil string, want int) string {
	t.Helper()
	output, code := s.plan(t, version, validUntil)
	if code != want {
		t.Fatalf(
			"plan for password version %s exited %d and the test expects %d:\n%s",
			version,
			code,
			want,
			output,
		)
	}
	return output
}

func (s *scenario) requireLogin(t *testing.T, version string, works bool) {
	t.Helper()
	err := s.target.loginError(t, s.role, s.password(version))
	if works && err != nil {
		t.Fatalf("a login with password version %s failed: %v", version, err)
	}
	if !works {
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "28P01" {
			t.Fatalf(
				"a login with password version %s returned %v; the test expects invalid password SQLSTATE 28P01",
				version,
				err,
			)
		}
	}
}

func (s *scenario) requireState(t *testing.T, version, validUntil string) {
	t.Helper()
	got := s.space.stateValues(t)
	if got.Version != version || got.ValidUntil != validUntil {
		t.Fatalf("state has password version %q and valid_until %q; the test expects %q and %q",
			got.Version, got.ValidUntil, version, validUntil)
	}
	if got.ID != s.role || got.Name != s.role {
		t.Fatalf(
			"state has id %q and name %q; the test expects both to equal %q",
			got.ID,
			got.Name,
			s.role,
		)
	}
}

func (s *scenario) requirePlanRequestsPassword(t *testing.T, output string) {
	t.Helper()
	if !strings.Contains(output, "password_wo_version") {
		t.Fatalf("the plan does not request a password version update:\n%s", output)
	}
}

func (s *scenario) secrets() []string {
	return []string{s.firstPassword, s.secondPassword, s.target.adminPassword}
}

// requireSavedPlanClean saves a plan file, decodes it with tofu show -json, and
// requires that the decoded plan contains no generated credential.
func (s *scenario) requireSavedPlanClean(t *testing.T, version, validUntil string) {
	t.Helper()
	variables := s.space.roleVariables(s.role, s.password(version), version, validUntil)
	_, code := s.space.run(t, variables, "plan", "-out="+savedPlanName, "-no-color", "-input=false")
	if code != exitSuccess {
		t.Fatalf("saving a plan exited %d", code)
	}
	decoded, code := s.space.run(t, nil, "show", "-json", savedPlanName)
	if code != exitSuccess {
		t.Fatalf("decoding the saved plan exited %d", code)
	}
	for _, secret := range s.secrets() {
		if strings.Contains(decoded, secret) {
			t.Fatalf("the decoded saved plan contains a generated credential")
		}
	}
}

func (s *scenario) requireNoSecrets(t *testing.T) {
	t.Helper()
	s.space.requireNoSecrets(t, s.secrets()...)
}
