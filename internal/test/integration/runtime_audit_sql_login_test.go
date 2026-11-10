package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"net/url"
	"testing"
	"text/template"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_change_login.sql
var runtimeAuditChangeLoginTemplate string

//go:embed testdata/runtime_audit_drop_login.sql
var runtimeAuditDropLoginTemplate string

const sqlAuditLoginPrefix = "tack_test_change_"

type sqlAuditLogin struct {
	Login  string
	Secret string
}

// changeOnlyPool opens a pool on the DSN from changeOnlyDSN. Test cleanup
// closes the pool and drops the login.
func changeOnlyPool(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	return testenv.LedgerPool(t, changeOnlyDSN(t, admin))
}

// changeOnlyDSN creates a login that can read and change users, api_tokens,
// and org_members but cannot execute public.ops_outbox_write_encoded. It
// returns the connection string of that login on the ledger that admin
// connects to. Test cleanup drops the login.
func changeOnlyDSN(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	return templateLoginDSN(t, admin, runtimeAuditChangeLoginTemplate, runtimeAuditDropLoginTemplate)
}

// templateLoginDSN creates a login with the createTemplate statements and
// returns its connection string on the ledger that admin connects to. Test
// cleanup runs the dropTemplate statements.
func templateLoginDSN(t *testing.T, admin *pgxpool.Pool, createTemplate, dropTemplate string) string {
	t.Helper()
	suffix := make([]byte, 4)
	secret := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate login suffix: got error %v, want nil", err)
	}
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate login password: got error %v, want nil", err)
	}
	login := sqlAuditLogin{Login: sqlAuditLoginPrefix + hex.EncodeToString(suffix), Secret: hex.EncodeToString(secret)}
	execLoginTemplate(t, admin, createTemplate, login)
	t.Cleanup(func() { execLoginTemplate(t, admin, dropTemplate, login) })
	parsed, err := url.Parse(admin.Config().ConnString())
	if err != nil {
		t.Fatalf("parse ledger connection string: got error %v, want nil", err)
	}
	parsed.User = url.UserPassword(login.Login, login.Secret)
	return parsed.String()
}

func execLoginTemplate(t *testing.T, admin *pgxpool.Pool, text string, login sqlAuditLogin) {
	t.Helper()
	parsed, err := template.New("login").Parse(text)
	if err != nil {
		t.Fatalf("parse login template: got error %v, want nil", err)
	}
	var statements bytes.Buffer
	if err := parsed.Execute(&statements, login); err != nil {
		t.Fatalf("render login statements: got error %v, want nil", err)
	}
	if _, err := admin.Exec(context.WithoutCancel(t.Context()), statements.String()); err != nil {
		t.Fatalf("run login statements for %s: got error %v, want nil", login.Login, err)
	}
}
