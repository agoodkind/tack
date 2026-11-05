//go:build pgoverlay

package pgoverlay_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestYugabyteExternalRenameUnchangedPassword(t *testing.T) {
	s := externalScenario(t)
	ctx, cancel := context.WithTimeout(t.Context(), databaseTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, s.target.connectionString(s.target.adminUser, s.target.adminPassword))
	if err != nil {
		t.Fatalf("connect to check password encryption: %v", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var encryption string
	if err := conn.QueryRow(ctx, "SHOW password_encryption").Scan(&encryption); err != nil {
		t.Fatalf("read password encryption: %v", err)
	}
	if encryption != "md5" {
		t.Fatalf("password encryption is %q; the test requires md5", encryption)
	}
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	oid := s.target.roleOID(t, s.role)
	if output, code := s.applyRole(t, renamed, "1", infinity); code != exitSuccess {
		t.Fatalf("rename exited %d:\n%s", code, output)
	}
	if name, found := s.target.roleNameByOID(t, oid); !found || name != renamed {
		t.Fatalf("role OID %d has name %q (found %t), expected %s", oid, name, found, renamed)
	}
	got := s.space.stateValues(t)
	if got.Name != renamed || got.ID != renamed || got.OID != oid || got.Version != "1" {
		t.Fatalf("state is %+v; expected name and id %s, OID %d, and version 1", got, renamed, oid)
	}
	if err := s.target.loginError(t, renamed, s.password("1")); err != nil {
		t.Fatalf("login after rename failed: %v", err)
	}
	s.planRole(t, renamed, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}
