package testenv

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// KeywordDSN returns a keyword connection string with host= listing the
// fixed address of every node of the cluster in node order, for login with
// secret. The test process dials addresses because it resolves no node name.
func (c *LedgerCluster) KeywordDSN(login, secret string) string {
	hosts := make([]string, 0, len(c.names))
	for _, name := range c.names {
		hosts = append(hosts, c.addresses[name].String())
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		strings.Join(hosts, ","), ledgerPort, login, secret, ledgerDatabase)
}

// AdminSecret returns the superuser key every node was started with.
func (c *LedgerCluster) AdminSecret() string {
	return c.superuserKey
}

// Admin runs one yb-admin subcommand in the first node against the masters
// of the started nodes and returns its output. The master list contains node
// names, which the first node resolves through its hosts entries. A non-zero
// exit is an error.
func (c *LedgerCluster) Admin(ctx context.Context, subcommand string) (string, error) {
	addresses := make([]string, 0, len(c.started))
	for _, name := range c.started {
		addresses = append(addresses, name+":"+ledgerMasterPort)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	command := []string{ledgerClusterAdmin, "--master_addresses", strings.Join(addresses, ","), subcommand}
	output, code, err := execInContainer(readCtx, c.cli, c.containers[c.names[0]], command)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return output, fmt.Errorf("yb-admin %s exited %d: %s", subcommand, code, output)
	}
	return output, nil
}
