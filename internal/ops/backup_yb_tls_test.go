package ops

import (
	"context"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestYBAdminClusterAccessAddsCertificatesForTLS checks
// master-address arguments and TLS certificate flags with read-only mounts.
// yb-admin uses the cluster protocol rather than SQL connection settings.
// Missing directories must not create empty Docker mounts or invented paths.
func TestYBAdminClusterAccessAddsCertificatesForTLS(t *testing.T) {
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
				"--master_addresses",
				masters,
				"--certs_dir_name",
				certsDir,
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
		t.Run(
			test.name,
			func(t *testing.T) {
				args, binds := ybAdminClusterAccess(test.cfg)
				if !slices.Equal(args, test.wantArgs) {
					t.Errorf("args = %v, want %v", args, test.wantArgs)
				}

				if !slices.Equal(binds, test.wantBinds) {
					t.Errorf("binds = %v, want %v", binds, test.wantBinds)
				}
			},
		)
	}
}

// TestYBDumpTransportVerifiesTheNodeOnlyWhenTheClusterEncrypts checks libpq
// environment settings for schema and role dumps against TLS ledger nodes.
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
			// Verification remains mandatory without a directory. Connection
			// failure must replace the client's unverified default mode.
			name:      "encrypted cluster with no directory rendered",
			cfg:       &config.Config{LedgerTLSEnabled: true, LedgerCertsDir: ""},
			wantEnv:   []string{"PGSSLMODE=verify-full"},
			wantBinds: nil,
		},
	}

	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				env, binds := ybDumpTransport(test.cfg)
				if !slices.Equal(env, test.wantEnv) {
					t.Errorf("env = %v, want %v", env, test.wantEnv)
				}

				if !slices.Equal(binds, test.wantBinds) {
					t.Errorf("binds = %v, want %v", binds, test.wantBinds)
				}
			},
		)
	}
}

// TestRunYBDumpOneShotRefusesAnEncryptedLedgerWithNoCertificateDirectory
// checks rejection before container creation when node verification lacks a
// certificate directory.
func TestRunYBDumpOneShotRefusesAnEncryptedLedgerWithNoCertificateDirectory(t *testing.T) {
	cfg := &config.Config{
		LedgerTLSEnabled:        true,
		LedgerCertsDir:          "",
		BackupYBMasterAddresses: "yb1:7100",
	}
	spec := ybDumpSpec{label: "schema", failEvent: "backup.yb_snapshot.schema_failed"}

	err := runYBDumpOneShot(context.Background(), nil, cfg, t.TempDir(), spec)
	if err == nil {
		t.Fatal(
			"runYBDumpOneShot did not reject TLS without a certificate directory",
		)
	}

	if !strings.Contains(err.Error(), "TACK_LEDGER_CERTS_DIR") {
		t.Errorf("error = %q, want the missing setting", err.Error())
	}
}

// TestLedgerCertificateSettingsRejectBlankDirectory checks that
// whitespace-only values cannot create certificate mounts or tool arguments.
func TestLedgerCertificateSettingsRejectBlankDirectory(t *testing.T) {
	cfg := &config.Config{
		LedgerTLSEnabled:        true,
		LedgerCertsDir:          "   ",
		BackupYBMasterAddresses: "yb1:7100",
	}

	args, binds := ybAdminClusterAccess(cfg)
	if slices.Contains(args, "--certs_dir_name") {
		t.Errorf("args = %v, certificate flag must not use a blank directory", args)
	}

	if len(binds) != 0 {
		t.Errorf("binds = %v, want an empty list for a blank directory", binds)
	}

	env, dumpBinds := ybDumpTransport(cfg)
	if !slices.Equal(env, []string{"PGSSLMODE=verify-full"}) {
		t.Errorf("env = %v, want the verification mode alone", env)
	}

	if len(dumpBinds) != 0 {
		t.Errorf("dump binds = %v, want an empty list for a blank directory", dumpBinds)
	}

	spec := ybDumpSpec{label: "schema", failEvent: "backup.yb_snapshot.schema_failed"}
	if err := runYBDumpOneShot(context.Background(), nil, cfg, t.TempDir(), spec); err == nil {
		t.Error("runYBDumpOneShot did not reject a blank certificate directory")
	}
}
