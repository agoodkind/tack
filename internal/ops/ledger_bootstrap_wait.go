// ledger_bootstrap_wait.go declares `ops ledger bootstrap-wait`: on a ledger
// bootstrap run, after a joining node starts, the deploy waits here until the
// first nodes of the ledger node list report the expected masters and tablet
// servers, and on the last join the expected replica count with no
// under-replicated tablet. The wait is bounded by one deadline.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const (
	// ledgerBootstrapWaitDefaultDeadline bounds a wait that checks only the
	// master and tablet server counts. TestLedgerBootstrapCluster on 347dab0b
	// (Mac runner, 2026-10-02) measured 3.1 s from the yb2 start to 2 and 2
	// and 5.8 s from the yb3 start to 3 and 3; Mira M5 measured 10.6 s for a
	// yb2 join. One minute is about 5.7 times the slowest of the three.
	ledgerBootstrapWaitDefaultDeadline = time.Minute
	// ledgerBootstrapReplicationDeadline bounds a wait with --replicas. The
	// same run measured 204.3 s from the yb3 start to 0 under-replicated
	// tablets on the migrated ledger (44 tablets). Eleven minutes is three
	// times that, rounded up to a whole minute, for QA guests that replicate
	// across hosts under ledger TLS.
	ledgerBootstrapReplicationDeadline = 11 * time.Minute
	// ledgerBootstrapWaitDefaultPoll is how often the wait reads the cluster.
	ledgerBootstrapWaitDefaultPoll = 2 * time.Second
)

// ledgerBootstrapWaitInput is the command's flags. Deadline and Poll are Go
// durations; an empty Deadline selects the default for the replica check.
type ledgerBootstrapWaitInput struct {
	clispec.InputMarker
	Masters       int
	TabletServers int
	Replicas      int
	Deadline      string
	Poll          string
}

// ledgerBootstrapTarget is the cluster state the wait requires. Replicas 0
// skips the replica and under-replicated tablet reads.
type ledgerBootstrapTarget struct {
	masters       int
	tabletServers int
	replicas      int
}

// ledgerBootstrapWaitOp declares `ops ledger bootstrap-wait`.
func ledgerBootstrapWaitOp(f *cli.Factory) clispec.Operation[ledgerBootstrapWaitInput] {
	return clispec.Operation[ledgerBootstrapWaitInput]{
		Name:     clispec.Name{Canonical: "bootstrap-wait", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsLedgerBootstrapWait), Reads: true},
		Group:    ledgerGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Wait until the first --masters ledger nodes report the expected masters, tablet servers, and replicas",
		Long: "Runs yb-admin in one-shot containers against the first --masters names of " +
			"TACK_LEDGER_NODE_HOSTS on port 7100 and counts the ALIVE rows of list_all_masters " +
			"and list_all_tablet_servers. With --replicas it also reads numReplicas from " +
			"get_universe_config and the under-replicated tablet count from the master health " +
			"check, which must be 0. A failed read is retried. At the deadline it fails and " +
			"prints the elapsed time and every count.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[ledgerBootstrapWaitInput]{
			clispec.IntParam("masters", "required: ALIVE masters to wait for, and how many ledger nodes to ask", 0,
				func(in *ledgerBootstrapWaitInput, v int) { in.Masters = v }),
			clispec.IntParam("tablet-servers", "required: ALIVE tablet servers to wait for", 0,
				func(in *ledgerBootstrapWaitInput, v int) { in.TabletServers = v }),
			clispec.IntParam("replicas", "live replica count to wait for, with 0 under-replicated tablets; 0 skips both", 0,
				func(in *ledgerBootstrapWaitInput, v int) { in.Replicas = v }),
			clispec.StringParam("deadline", "total time before the wait fails (default 1m, or 11m with --replicas)", "", false,
				func(in *ledgerBootstrapWaitInput, v string) { in.Deadline = v }),
			clispec.StringParam("poll", "how often to read the cluster", ledgerBootstrapWaitDefaultPoll.String(), false,
				func(in *ledgerBootstrapWaitInput, v string) { in.Poll = v }),
		},
		New: func() ledgerBootstrapWaitInput {
			return ledgerBootstrapWaitInput{
				InputMarker: clispec.InputMarker{}, Masters: 0, TabletServers: 0, Replicas: 0,
				Deadline: "", Poll: ledgerBootstrapWaitDefaultPoll.String(),
			}
		},
		Run: func(ctx context.Context, in ledgerBootstrapWaitInput, sink clispec.ResultSink) error {
			target, deadline, poll, err := ledgerBootstrapWaitSettings(in)
			if err != nil {
				slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.failed", slog.String("err", err.Error()))
				return fmt.Errorf("%s: %w", ledgerBootstrapCommandLabel, err)
			}
			return runLedgerBootstrapWait(ctx, f.Cfg, sink, target, deadline, poll)
		},
	}
}

// ledgerBootstrapWaitSettings checks the flags and selects the deadline.
func ledgerBootstrapWaitSettings(in ledgerBootstrapWaitInput) (ledgerBootstrapTarget, time.Duration, time.Duration, error) {
	target := ledgerBootstrapTarget{masters: in.Masters, tabletServers: in.TabletServers, replicas: in.Replicas}
	var problems []string
	if in.Masters < 1 {
		problems = append(problems, fmt.Sprintf("--masters is required and must be at least 1, got %d", in.Masters))
	}
	if in.TabletServers < 1 {
		problems = append(problems, fmt.Sprintf("--tablet-servers is required and must be at least 1, got %d", in.TabletServers))
	}
	if in.Replicas < 0 {
		problems = append(problems, fmt.Sprintf("--replicas must be 0 or more, got %d", in.Replicas))
	}
	deadline := ledgerBootstrapWaitDefaultDeadline
	if in.Replicas > 0 {
		deadline = ledgerBootstrapReplicationDeadline
	}
	if in.Deadline != "" {
		parsed, err := time.ParseDuration(in.Deadline)
		if err != nil || parsed <= 0 {
			problems = append(problems, fmt.Sprintf("--deadline %q is not a positive duration", in.Deadline))
		}
		deadline = parsed
	}
	poll, err := time.ParseDuration(in.Poll)
	if err != nil || poll <= 0 {
		problems = append(problems, fmt.Sprintf("--poll %q is not a positive duration", in.Poll))
	}
	if len(problems) > 0 {
		return ledgerBootstrapTarget{}, 0, 0, errors.New(strings.Join(problems, "; "))
	}
	return target, deadline, poll, nil
}
