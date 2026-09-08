package ops

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestYBAdminClusterAccessCarriesCertificatesOnlyWhenTheClusterEncrypts pins
// what the engine's administration tool is told about this environment. On a
// plaintext cluster it gets the master addresses and nothing else, so no
// one-shot mounts a directory that does not exist; on an encrypted cluster it
// also gets the certificate directory as a flag and as a read-only mount at
// the same path, which is the whole reason a backup can still reach the
// cluster after TACK-460 (the tool speaks the cluster protocol, not SQL, so
// the DSNs' sslmode does nothing for it).
//
// The third case is a half-configured host: encryption on, no directory
// rendered. It must not invent a path, because a mount of a directory Docker
// then creates empty would turn a clear connection failure into a confusing
// certificate error.
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

// TestYBDumpTransportVerifiesTheNodeOnlyWhenTheClusterEncrypts pins what the
// schema and roles dumps are told. The dumpers take no connection-string flag,
// so an encrypted ledger has to reach them through the environment; without
// this they connect in the clear and an encrypted node refuses them, which
// would fail the nightly export rather than a test.
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
			name:      "encrypted cluster with no directory rendered",
			cfg:       &config.Config{LedgerTLSEnabled: true, LedgerCertsDir: ""},
			wantEnv:   nil,
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
