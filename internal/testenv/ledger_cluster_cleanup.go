package testenv

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// ledgerClusterLogDirectory is the yugabyted log directory under the
// --base_dir that Start passes.
const ledgerClusterLogDirectory = "/home/yugabyte/var/logs"

// ledgerClusterProcessLogs are the yb-master and yb-tserver logs that
// yugabyted writes under ledgerClusterLogDirectory.
var ledgerClusterProcessLogs = []string{
	ledgerClusterLogDirectory + "/master/yb-master.INFO",
	ledgerClusterLogDirectory + "/tserver/yb-tserver.INFO",
}

// remove writes the node diagnostics when the test failed, removes every
// started node, then detaches the test process from the cluster network and
// removes the network.
func (c *LedgerCluster) remove(t *testing.T) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	if t.Failed() {
		c.logDiagnostics(cleanup, t)
	}
	containers := make([]string, 0, len(c.started))
	for _, name := range c.started {
		containers = append(containers, c.containers[name])
	}
	if err := removeContainers(cleanup, containers); err != nil {
		t.Errorf("remove ledger cluster nodes: %v", err)
	}
	if err := removeLedgerClusterNetwork(cleanup, c.cli, c.Network, c.selfID); err != nil {
		t.Errorf("remove ledger cluster network: %v", err)
	}
}

// logDiagnostics writes the state of every started node to the test log
// before the cleanup removes the nodes: the container state, the last
// engineLogLines lines of the container log, the tails of the yb-master and
// yb-tserver logs, and the masters and tablet servers that the first node
// lists. A read that fails writes its error in place of its output.
func (c *LedgerCluster) logDiagnostics(ctx context.Context, t *testing.T) {
	t.Helper()
	var report strings.Builder
	for _, name := range c.started {
		containerName := c.containers[name]
		fmt.Fprintf(&report, "== ledger node %s (container %s, address %s)\n", name, containerName, c.addresses[name])
		running := c.writeNodeState(ctx, &report, containerName)
		logs, err := engineLogTail(ctx, c.cli, containerName)
		fmt.Fprintf(&report, "-- container log, last %s lines (read error %v):\n%s\n", engineLogLines, err, logs)
		if !running {
			report.WriteString("-- process logs: the container is not running\n")
			continue
		}
		for _, path := range ledgerClusterProcessLogs {
			output, code, err := execInContainer(ctx, c.cli, containerName, []string{"tail", "-n", engineLogLines, path})
			fmt.Fprintf(&report, "-- %s, last %s lines (exit %d, error %v):\n%s\n", path, engineLogLines, code, err, output)
		}
	}
	for _, subcommand := range []string{"list_all_masters", "list_all_tablet_servers"} {
		output, err := c.Admin(ctx, subcommand)
		fmt.Fprintf(&report, "== yb-admin %s from %s (error %v):\n%s\n", subcommand, c.names[0], err, output)
	}
	t.Logf("ledger cluster diagnostics after the failure:\n%s", report.String())
}

// writeNodeState writes the container state of containerName to report and
// returns whether the container runs.
func (c *LedgerCluster) writeNodeState(ctx context.Context, report *strings.Builder, containerName string) bool {
	inspected, err := c.cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{Size: false})
	if err != nil {
		fmt.Fprintf(report, "-- state unavailable: %v\n", err)
		return false
	}
	state := inspected.Container.State
	if state == nil {
		report.WriteString("-- state unavailable: the inspect result has no state\n")
		return false
	}
	fmt.Fprintf(report, "-- state: status=%s running=%t oom_killed=%t exit_code=%d error=%q started_at=%s finished_at=%s\n",
		state.Status, state.Running, state.OOMKilled, state.ExitCode, state.Error, state.StartedAt, state.FinishedAt)
	return state.Running
}
