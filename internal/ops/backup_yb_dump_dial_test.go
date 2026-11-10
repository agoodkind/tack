package ops

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestYBDumpDialUsesTheNodeNameOnlyWhenTheClusterEncrypts checks the dump's
// node selection. Plaintext uses the master-list address without an extra
// hosts-file entry. Encryption uses the permanent node name and maps it to
// the same address. TACK-460 QA found that the dumper's client rejected
// address-based certificate verification.
//
// An address without a known name remains an address. An incomplete name map
// then leaves certificate validation to the client instead of inventing an
// unresolvable hostname. This test checks the returned host and mapping; it
// does not establish a TLS connection.
func TestYBDumpDialUsesTheNodeNameOnlyWhenTheClusterEncrypts(t *testing.T) {
	const hosts = "yb1=3d06:bad:b01:210::220, yb2=3d06:bad:b01:210::221,yb3=3d06:bad:b01:210::222"

	tests := []struct {
		name       string
		cfg        *config.Config
		address    string
		wantHost   string
		wantExtras []string
	}{
		{
			name:       "plaintext cluster dials the address",
			cfg:        &config.Config{LedgerTLSEnabled: false, LedgerNodeHosts: hosts},
			address:    "3d06:bad:b01:210::220",
			wantHost:   "3d06:bad:b01:210::220",
			wantExtras: nil,
		},
		{
			name:       "encrypted cluster dials the name and maps it",
			cfg:        &config.Config{LedgerTLSEnabled: true, LedgerNodeHosts: hosts},
			address:    "3d06:bad:b01:210::221",
			wantHost:   "yb2",
			wantExtras: []string{"yb2:3d06:bad:b01:210::221"},
		},
		{
			name:       "encrypted cluster with no name for the address dials the address",
			cfg:        &config.Config{LedgerTLSEnabled: true, LedgerNodeHosts: hosts},
			address:    "3d06:bad:b01:210::9",
			wantHost:   "3d06:bad:b01:210::9",
			wantExtras: nil,
		},
		{
			name:       "encrypted cluster with no map dials the address",
			cfg:        &config.Config{LedgerTLSEnabled: true, LedgerNodeHosts: ""},
			address:    "3d06:bad:b01:210::220",
			wantHost:   "3d06:bad:b01:210::220",
			wantExtras: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, extras := ybDumpDial(test.cfg, test.address)
			if host != test.wantHost {
				t.Errorf("host = %q, want %q", host, test.wantHost)
			}
			if !slices.Equal(extras, test.wantExtras) {
				t.Errorf("extra hosts = %v, want %v", extras, test.wantExtras)
			}
		})
	}
}

// TestLedgerNodeNamesSkipsMalformedEntries checks that blank entries and
// entries without a separator do not add nodes. A stray comma must not
// create a dump hostname.
func TestLedgerNodeNamesSkipsMalformedEntries(t *testing.T) {
	cfg := &config.Config{LedgerNodeHosts: "yb1=::220,,yb2,=::222, yb3 = ::223 "}
	names := ledgerNodeNames(cfg)
	if len(names) != 2 {
		t.Fatalf("names = %v, want two entries", names)
	}
	if names["::220"] != "yb1" || names["::223"] != "yb3" {
		t.Errorf("names = %v, want yb1 at ::220 and yb3 at ::223", names)
	}
}
