package ops

import (
	"context"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestYBAdminClusterAccessCarriesCertificatesOnlyWhenTheClusterEncrypts checks
// yb-admin cluster-access arguments and mounts.
//
// The test checks these configurations:
//   - Plaintext clusters use master addresses without a certificate mount.
//   - Encrypted clusters also use a certificate flag and a read-only mount at
//     the host path.
//   - Encrypted clusters without a rendered directory omit the certificate flag
//     and mount instead of inventing a path.
//
// TACK-460 introduced cluster encryption. yb-admin uses the cluster protocol
// rather than SQL; a SQL connection string's sslmode does not configure it.
// Inventing a directory could let Docker create an empty mount and obscure the
// missing configuration with a certificate error.
func TestYBAdminClusterAccessCarriesCertificatesOnlyWhenTheClusterEncrypts(t *testing.T) {
	const masters = "yb1:7100,yb2:7100,yb3:7100"
	const certsDir = "/etc/tack/ledger-certs"

	tests := []struct {
		name      string
		cfg       *config.Config
		wantArgs  []string
		wantBinds []string
	}{
		{
			name: "plaintext cluster",
			cfg: &config.Config{
				BackupYBMasterAddresses: masters,
				LedgerTLSEnabled:        false,
				LedgerCertsDir:          certsDir,
			},
			wantArgs:  []string{"--master_addresses", masters},
			wantBinds: nil,
		},
		{
			name: "encrypted cluster",
			cfg: &config.Config{
				BackupYBMasterAddresses: masters,
				LedgerTLSEnabled:        true,
				LedgerCertsDir:          certsDir,
			},
			wantArgs: []string{
				"--master_addresses", masters,
				"--certs_dir_name", certsDir,
			},
			wantBinds: []string{certsDir + ":" + certsDir + ":ro"},
		},
		{
			name: "encrypted cluster with no directory rendered",
			cfg: &config.Config{
				BackupYBMasterAddresses: masters,
				LedgerTLSEnabled:        true,
				LedgerCertsDir:          "",
			},
			wantArgs:  []string{"--master_addresses", masters},
			wantBinds: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, binds := ybAdminClusterAccess(test.cfg)
			if !slices.Equal(args, test.wantArgs) {
				t.Errorf("args = %v, want %v", args, test.wantArgs)
			}
			if !slices.Equal(binds, test.wantBinds) {
				t.Errorf("binds = %v, want %v", binds, test.wantBinds)
			}
		})
	}
}

// TestYBDumpTransportVerifiesTheNodeOnlyWhenTheClusterEncrypts checks the
// transport environment and mounts for schema and roles dumps. The dumpers do
// not accept a connection-string flag. Their environment configures encrypted
// connections and node verification; plaintext connections to an encrypted node
// would fail the nightly export.
func TestYBDumpTransportVerifiesTheNodeOnlyWhenTheClusterEncrypts(t *testing.T) {
	const certsDir = "/etc/tack/ledger-certs"

	tests := []struct {
		name      string
		cfg       *config.Config
		wantEnv   []string
		wantBinds []string
	}{
		{
			name:      "plaintext cluster",
			cfg:       &config.Config{LedgerTLSEnabled: false, LedgerCertsDir: certsDir},
			wantEnv:   nil,
			wantBinds: nil,
		},
		{
			name: "encrypted cluster",
			cfg:  &config.Config{LedgerTLSEnabled: true, LedgerCertsDir: certsDir},
			wantEnv: []string{
				"PGSSLMODE=verify-full",
				"PGSSLROOTCERT=" + certsDir + "/ca.crt",
			},
			wantBinds: []string{certsDir + ":" + certsDir + ":ro"},
		},
		{
			// The helper omits mounts when the certificate directory is unset but still
			// requests verification. It must not use the client library's default mode,
			// which could accept an unverified connection.
			name:      "encrypted cluster with no directory rendered",
			cfg:       &config.Config{LedgerTLSEnabled: true, LedgerCertsDir: ""},
			wantEnv:   []string{"PGSSLMODE=verify-full"},
			wantBinds: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, binds := ybDumpTransport(test.cfg)
			if !slices.Equal(env, test.wantEnv) {
				t.Errorf("env = %v, want %v", env, test.wantEnv)
			}
			if !slices.Equal(binds, test.wantBinds) {
				t.Errorf("binds = %v, want %v", binds, test.wantBinds)
			}
		})
	}
}

// TestRunYBDumpOneShotRefusesAnEncryptedLedgerWithNoCertificateDirectory checks
// that the dump command rejects missing certificate configuration before
// starting a container. An encrypted ledger requires a certificate directory to
// verify the node before exporting data.
func TestRunYBDumpOneShotRefusesAnEncryptedLedgerWithNoCertificateDirectory(t *testing.T) {
	cfg := &config.Config{
		LedgerTLSEnabled:        true,
		LedgerCertsDir:          "",
		BackupYBMasterAddresses: "yb1:7100",
	}
	spec := ybDumpSpec{label: "schema", failEvent: "backup.yb_snapshot.schema_failed"}

	err := runYBDumpOneShot(context.Background(), nil, cfg, t.TempDir(), spec)
	if err == nil {
		t.Fatal("runYBDumpOneShot returned no error for an encrypted ledger with no certificate directory")
	}
	if !strings.Contains(err.Error(), "TACK_LEDGER_CERTS_DIR") {
		t.Errorf("error = %q, want it to name the missing setting", err.Error())
	}
}

// TestLedgerCertificateSettingsTreatABlankDirectoryAsNone checks that
// administration arguments, dump transport, and dump startup treat whitespace
// as an unset certificate directory. An environment file can hide a stray
// space; a path made of spaces does not supply valid certificates. Review of
// the original branch identified this case.
func TestLedgerCertificateSettingsTreatABlankDirectoryAsNone(t *testing.T) {
	cfg := &config.Config{
		LedgerTLSEnabled:        true,
		LedgerCertsDir:          "   ",
		BackupYBMasterAddresses: "yb1:7100",
	}

	args, binds := ybAdminClusterAccess(cfg)
	if slices.Contains(args, "--certs_dir_name") {
		t.Errorf("args = %v, want no certificate flag for a blank directory", args)
	}
	if len(binds) != 0 {
		t.Errorf("binds = %v, want none for a blank directory", binds)
	}

	env, dumpBinds := ybDumpTransport(cfg)
	if !slices.Equal(env, []string{"PGSSLMODE=verify-full"}) {
		t.Errorf("env = %v, want the verification mode alone", env)
	}
	if len(dumpBinds) != 0 {
		t.Errorf("dump binds = %v, want none for a blank directory", dumpBinds)
	}

	spec := ybDumpSpec{label: "schema", failEvent: "backup.yb_snapshot.schema_failed"}
	if err := runYBDumpOneShot(context.Background(), nil, cfg, t.TempDir(), spec); err == nil {
		t.Error("runYBDumpOneShot returned no error for a blank certificate directory")
	}
}
