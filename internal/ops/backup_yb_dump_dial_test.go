package ops

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestYBDumpDialUsesTheNodeNameOnlyWhenTheClusterEncrypts pins how a dump
// one-shot addresses a ledger node. In the clear it dials the address the
// master list carries and needs nothing else. Under encryption it dials the
// node's permanent name, mapped to that same address in its own hosts file,
// because the dumper's client library verifies a certificate by name only and
// refused the address on the QA proof. An address with no known name still
// dials as an address, so a half-rendered map fails on the certificate rather
// than on a name that resolves nowhere.
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

// TestLedgerNodeNamesSkipsMalformedEntries pins that a blank entry or one
// without a separator adds no node, so a stray comma in the rendered value
// cannot become a name the dump dials.
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
