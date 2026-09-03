// backup_yb_dump_endpoints.go decides which node a SQL dump talks to, and what
// happens when one will not serve it.
//
// The dump used to be pinned to the first entry of master_addresses, which made
// both SQL artifacts depend on one named node: with that node down, the export
// produced neither the schema nor the roles even though the remaining nodes
// held quorum and the cluster was serving reads and writes. A backup available
// only while one particular node is up is less available than the data it backs
// up. So a dump walks the configured nodes in order and stops at the first that
// hands back a usable file, and only a cluster where none of them will is a
// failure.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybDumpHosts returns the YSQL endpoints a dump may use, in the order
// master_addresses lists them, with ports stripped and repeats dropped. The
// master list is the cluster membership the export already trusts for
// yb-admin, and every node in it serves YSQL, so it is also the list of nodes
// that can answer a dump.
func ybDumpHosts(masterAddresses string) []string {
	var hosts []string
	seen := map[string]bool{}
	for entry := range strings.SplitSeq(masterAddresses, ",") {
		host := hostFromHostPort(strings.TrimSpace(entry))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

// ybDumpEndpointRefusal is one endpoint's failure to hand back the artifact,
// kept so a dump that runs out of endpoints can name each node it tried and
// what that node did.
type ybDumpEndpointRefusal struct {
	host   string
	reason string
}

// runYBDumpOneShot runs one dumper against the cluster's configured endpoints
// until one of them hands back the artifact.
func runYBDumpOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	spec ybDumpSpec,
) error {
	return walkYBDumpEndpoints(ctx, ybDumpHosts(cfg.BackupYBMasterAddresses), spec,
		func(host string) (int64, string) {
			return dumpYBFromEndpoint(ctx, cli, cfg, stageDir, spec, host)
		})
}

// walkYBDumpEndpoints tries hosts in order and returns as soon as one produces
// a usable file. Each refusal is logged as it happens, so a dump that
// succeeded on a later node still leaves a record that an earlier one would
// not serve it, which is how an operator learns a node is down from a run that
// otherwise passed.
//
// Exhausting the endpoints is a hard failure naming every host tried and what
// each one did. There is no partial success: the caller uploads nothing without
// the artifact, and an export that shipped without it would restore into a
// database missing whatever the artifact described. dump is a closure so the
// walk stays testable without a cluster.
func walkYBDumpEndpoints(
	ctx context.Context,
	hosts []string,
	spec ybDumpSpec,
	dump func(host string) (size int64, reason string),
) error {
	logger := telemetry.L(ctx)
	if len(hosts) == 0 {
		wrapped := fmt.Errorf("ysql %s dump: the configured master addresses name no endpoint to dump from",
			spec.label)
		logger.ErrorContext(ctx, spec.failEvent, slog.String("err", wrapped.Error()))
		return wrapped
	}
	refusals := make([]ybDumpEndpointRefusal, 0, len(hosts))
	servedBy := ""
	var served int64
	for _, host := range hosts {
		size, reason := dump(host)
		if reason == "" {
			servedBy, served = host, size
			break
		}
		refusals = append(refusals, ybDumpEndpointRefusal{host: host, reason: reason})
		logger.WarnContext(ctx, spec.attemptEvent,
			slog.String("host", host),
			slog.String("reason", reason),
			slog.Int("endpoints_left", len(hosts)-len(refusals)))
	}
	if servedBy == "" {
		described := make([]string, 0, len(refusals))
		for _, refusal := range refusals {
			described = append(described, refusal.host+": "+refusal.reason)
		}
		wrapped := fmt.Errorf("ysql %s dump: no endpoint served it, tried %s: %s",
			spec.label, strings.Join(hosts, ", "), strings.Join(described, "; "))
		logger.ErrorContext(ctx, spec.failEvent, slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, spec.okEvent,
		slog.String("host", servedBy),
		slog.String("path", spec.outPath),
		slog.Int64("bytes", served))
	return nil
}

// dumpYBFromEndpoint runs one dumper against one endpoint in a one-shot
// container the same way the yb-admin one-shots run, with the stage dir
// bind-mounted. It reports the artifact's size, or the reason this endpoint did
// not produce one; a dump that wrote nothing counts as a refusal rather than a
// success, because an empty artifact restores as an absence nobody notices.
//
// The reason is not inspected to decide whether to move on. Telling a node that
// is down apart from a dump that would fail everywhere means reading the
// dumper's own error text, which the client renders in its message locale, so
// every refusal is treated the same and the endpoints simply run out. The cost
// of that is a few one-shots on a dump that was never going to work, and what
// it buys is that the decision never depends on a translated sentence.
func dumpYBFromEndpoint(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	spec ybDumpSpec,
	host string,
) (size int64, reason string) {
	logger := telemetry.L(ctx)
	cmd := append([]string{"-h", host, "-p", ybDumpPort, "-U", cfg.YugabyteUser}, spec.args...)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{spec.binary},
		Cmd:        cmd,
		Env:        []string{"PGPASSWORD=" + cfg.YugabytePassword},
		Binds:      []string{stageDir + ":" + ybDumpOutDir},
		ExtraHosts: nil,
		Name:       "",
	})
	return ybDumpAttemptOutcome(res, err, spec.outPath)
}

// ybDumpAttemptOutcome reads one attempt's result and the file it was supposed
// to leave behind, and reports the artifact's size or why there is no artifact.
// A dump that exited cleanly but wrote nothing counts as a refusal like any
// other, because an empty artifact restores as an absence nobody notices, and
// because a later endpoint may still write a real one.
func ybDumpAttemptOutcome(res execResult, runErr error, outPath string) (size int64, reason string) {
	if runErr != nil {
		return 0, "dump one-shot: " + runErr.Error()
	}
	if res.ExitCode != 0 {
		return 0, fmt.Sprintf("exited %d: %s", res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, fmt.Sprintf("stat %s: %s", outPath, err.Error())
	}
	if info.Size() == 0 {
		return 0, "produced 0 bytes"
	}
	return info.Size(), ""
}
