// backup_yb_dump_dial.go decides what host name a SQL dump one-shot dials for
// a ledger node, and what its container needs to resolve that name.
//
// The dumpers run inside the engine's own image, whose client library checks a
// server certificate against the host it dialed by name only: it never matches
// an address against the certificate, however the certificate names it. The
// export walks the nodes by the addresses the master list carries, so under
// encryption a dump that dialed an address was refused by a certificate that
// named the node correctly (found on the QA proof of TACK-460). Dialing the
// node's permanent name instead, with that name mapped to the same address in
// the one-shot's hosts file, reaches the same node and verifies its
// certificate the way every other client does.

package ops

import (
	"strings"

	"goodkind.io/tack/internal/config"
)

// ybDumpDial returns the host a dump one-shot dials to reach the ledger node
// at address, and the hosts-file entry the one-shot needs to resolve it. In
// the clear, or where no name is known for the address, the dump dials the
// address itself and needs no entry.
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

// ledgerNodeNames parses the deploy's name=address pairs into a map from each
// address to the node name it belongs to. Entries that are blank or carry no
// separator are skipped rather than made into a node.
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
