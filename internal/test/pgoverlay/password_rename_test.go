//go:build pgoverlay

package pgoverlay_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestYugabyteExternalRenameUnchangedPassword(t *testing.T) {
	s := externalScenario(t)
	requireMD5(t, s.target)
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

func TestYugabyteExternalInterruptedRenameUnchangedPassword(t *testing.T) {
	s := externalScenario(t)
	requireMD5(t, s.target)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	oid := s.target.roleOID(t, s.role)
	if got := s.space.stateValues(t); got.AppliedName != s.role {
		t.Fatalf("password checkpoint is %q; expected %s", got.AppliedName, s.role)
	}
	proxy := newFaultProxy(t, s.target, s.role, "rename")
	s.space.target = proxy.target(s.target)
	output, code := s.applyRole(t, renamed, "1", infinity)
	proxy.recover(t)
	proxy.mutex.Lock()
	deniedPassword := proxy.deniedPassword
	proxy.mutex.Unlock()
	if !deniedPassword {
		t.Fatal("the proxy did not deny the password statement after rename")
	}
	if code == exitSuccess {
		t.Fatalf("rename succeeded after the proxy denied the next operation:\n%s", output)
	}
	if name, found := s.target.roleNameByOID(t, oid); !found || name != renamed {
		t.Fatalf("role OID %d has name %q (found %t), expected %s", oid, name, found, renamed)
	}
	got := s.space.stateValues(t)
	if got.Name != s.role || got.OID != oid || got.Version != "1" || got.AppliedName != s.role {
		t.Fatalf("state is %+v; expected prior name %s, OID %d, and version 1", got, s.role, oid)
	}
	var databaseError *pgconn.PgError
	err := s.target.loginError(t, renamed, s.password("1"))
	if !errors.As(err, &databaseError) || databaseError.Code != "28P01" {
		t.Fatalf("login after interruption returned %v; expected invalid password SQLSTATE 28P01", err)
	}
	s.planRole(t, renamed, "1", infinity, exitChanges)
	if output, code := s.applyRole(t, renamed, "1", infinity); code != exitSuccess {
		t.Fatalf("retry exited %d:\n%s", code, output)
	}
	if err := s.target.loginError(t, renamed, s.password("1")); err != nil {
		t.Fatalf("login after retry failed: %v", err)
	}
	if count := s.target.roleCount(t, s.role, renamed); count != 1 {
		t.Fatalf("database has %d matching roles, expected 1", count)
	}
	got = s.space.stateValues(t)
	if got.Name != renamed || got.ID != renamed || got.OID != oid || got.Version != "1" {
		t.Fatalf("state is %+v; expected name and id %s, OID %d, and version 1", got, renamed, oid)
	}
	s.planRole(t, renamed, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

func requireMD5(t *testing.T, target databaseTarget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), databaseTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, target.connectionString(target.adminUser, target.adminPassword))
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
}
