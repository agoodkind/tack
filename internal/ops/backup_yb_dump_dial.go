// Dump containers select a ledger node's hostname and the hosts-file entry
// that resolves it. They use the engine image's client library, which checks
// the server certificate against a hostname instead of matching a literal
// address in this setup.
//
// The export obtains node addresses from the master list. During QA
// verification for TACK-460, dialing an address failed certificate validation
// even though the certificate included the node identity. The dump now uses
// the permanent node name and maps it to that address in the container's
// hosts file. This selects the same node for hostname verification.

package ops

import (
	"strings"

	"goodkind.io/tack/internal/config"
)

// ybDumpDial returns the dump hostname and any container hosts-file entry
// for the node at address. It returns the address without an extra entry
// when encryption is off or the address does not have a known node name.
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

// ledgerNodeNames reads the deployment's name=address pairs into an
// address-to-name map. It skips entries without a separator, name, or address.
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
