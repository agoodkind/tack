// SQL dump containers use ledger node names to verify TLS certificates.
// The engine image's client library checks hostnames, not literal addresses.
// LedgerNodeHosts maps each configured master address to its permanent name.
// Docker ExtraHosts resolves that name to the original address (TACK-460).

package ops

import (
	"strings"

	"goodkind.io/tack/internal/config"
)

// ybDumpDial returns the connection hostname and optional hosts-file mapping.
// Plaintext connections and addresses without a known name use the address
// directly without a mapping.
func ybDumpDial(cfg *config.Config, address string) (host string, extraHosts []string) {
	if !cfg.LedgerTLSEnabled {
		return address, nil
	}

	name, ok := ledgerNodeNames(cfg)[address]
	if !ok {
		return address, nil
	}

	return name, []string{name + ":" + address}
}

// ledgerNodeNames parses name=address pairs into an address-to-name map.
// It skips empty entries and entries without a separator, name, or address.
func ledgerNodeNames(cfg *config.Config) map[string]string {
	names := map[string]string{}

	for entry := range strings.SplitSeq(cfg.LedgerNodeHosts, ",") {
		name, address, found := strings.Cut(strings.TrimSpace(entry), "=")
		name = strings.TrimSpace(name)
		address = strings.TrimSpace(address)
		if !found || name == "" || address == "" {
			continue
		}

		names[address] = name
	}

	return names
}
