// SQL dumps try the configured nodes in order until one produces a usable
// file. They fail if every endpoint refuses.
//
// The earlier dump used only the first master_addresses entry. When that
// node stopped, the export could not produce schema or roles even while the
// remaining quorum served reads and writes. Trying other nodes avoids making
// the backup depend on the availability of one particular node.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybDumpHosts returns hosts from master_addresses in configured order. It
// removes ports, duplicate hosts, and empty entries. The export also uses
// this master list for yb-admin and expects these nodes to serve YSQL.
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

// ybDumpEndpointRefusal records an endpoint and its failure reason. If every
// endpoint refuses, the dump reports each attempted host and reason.
type ybDumpEndpointRefusal struct {
	host   string
	reason string
}

// runYBDumpOneShot tries the dumper against configured endpoints until one
// produces the artifact.
func runYBDumpOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	spec ybDumpSpec,
) error {
	// An encrypted ledger requires a certificate directory before the dump
	// starts. Without the certificate authority, the client default can accept
	// an unverified connection. Reject the missing setting before running a
	// container and identify TACK_LEDGER_CERTS_DIR in the error (TACK-460).
	if cfg.LedgerTLSEnabled && ledgerCertsDir(cfg) == "" {
		err := fmt.Errorf(
			"ysql %s dump: the ledger encrypts client traffic but "+
				"TACK_LEDGER_CERTS_DIR is empty, so the node cannot be verified",
			spec.label,
		)
		telemetry.L(ctx).ErrorContext(ctx, spec.failEvent, slog.String("err", err.Error()))

		return err
	}

	return walkYBDumpEndpoints(
		ctx,
		ybDumpHosts(cfg.BackupYBMasterAddresses),
		spec,
		func(host string) (int64, string) {
			return dumpYBFromEndpoint(ctx, cli, cfg, stageDir, spec, host)
		},
	)
}

// walkYBDumpEndpoints tries hosts in order and stops after the first usable
// file. It logs every refusal, including when a later node succeeds, to
// expose a node that cannot serve the dump.
//
// If every host refuses, the function fails with each host and reason. The
// caller must not upload a partial export without this artifact; a restore
// would lack its database definitions or roles. The dump callback permits
// tests without a running cluster.
func walkYBDumpEndpoints(
	ctx context.Context,
	hosts []string,
	spec ybDumpSpec,
	dump func(host string) (size int64, reason string),
) error {
	logger := telemetry.L(ctx)
	if len(hosts) == 0 {
		wrapped := fmt.Errorf(
			"ysql %s dump: the configured master addresses name no endpoint to dump from",
			spec.label,
		)
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
		logger.WarnContext(
			ctx,
			spec.attemptEvent,
			slog.String("host", host),
			slog.String("reason", reason),
			slog.Int("endpoints_left", len(hosts)-len(refusals)),
		)
	}

	if servedBy == "" {
		described := make([]string, 0, len(refusals))
		for _, refusal := range refusals {
			described = append(described, refusal.host+": "+refusal.reason)
		}

		wrapped := fmt.Errorf(
			"ysql %s dump: no endpoint served it, tried %s: %s",
			spec.label,
			strings.Join(hosts, ", "),
			strings.Join(described, "; "),
		)
		logger.ErrorContext(ctx, spec.failEvent, slog.String("err", wrapped.Error()))

		return wrapped
	}

	logger.InfoContext(
		ctx,
		spec.okEvent,
		slog.String("host", servedBy),
		slog.String("path", spec.outPath),
		slog.Int64("bytes", served),
	)

	return nil
}

// dumpYBFromEndpoint runs the dumper in a temporary container with the
// staging directory mounted, using the same container mechanism as yb-admin.
// It returns the artifact size or a refusal reason. A missing or empty file
// must not count as a usable restore artifact.
//
// The caller retries every refusal without interpreting the reason. Client
// error text depends on its message locale and cannot reliably distinguish
// one unavailable node from a failure that affects every node. This may run
// extra containers for a failure that another endpoint cannot fix.
func dumpYBFromEndpoint(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	spec ybDumpSpec,
	host string,
) (size int64, reason string) {
	logger := telemetry.L(ctx)
	dialHost, extraHosts := ybDumpDial(cfg, host)
	cmd := make([]string, 0, 6+len(spec.args))
	cmd = append(cmd, "-h", dialHost, "-p", ybDumpPort, "-U", migration.MigratorRole)
	cmd = append(cmd, spec.args...)
	tlsEnv, tlsBinds := ybDumpTransport(cfg)
	env := make([]string, 0, 1+len(tlsEnv))
	env = append(env, ybDumpPasswordVar+"="+cfg.MigratorPassword)
	env = append(env, tlsEnv...)
	mounts := make([]string, 0, 1+len(tlsBinds))
	mounts = append(mounts, stageDir+":"+ybDumpOutDir)
	mounts = append(mounts, tlsBinds...)
	res, err := runOneShot(
		ctx,
		cli,
		logger,
		runOneShotOptions{
			Image:      cfg.BackupYBImage,
			Network:    cfg.BackupFDBNetwork,
			Entrypoint: []string{spec.binary},
			Cmd:        cmd,
			Env:        env,
			Binds:      mounts,
			ExtraHosts: extraHosts,
			Name:       "",
			User:       stageWriterUser(),
		},
	)

	return ybDumpAttemptOutcome(res, err, spec.outPath)
}

// ybDumpTransport returns TLS environment settings and a certificate mount
// for the dump container (TACK-460). The dumpers use environment variables
// instead of a connection-string flag. ybDumpDial selects a known node name
// for full certificate verification and maps it to the pinned address; the
// node certificate includes its name and address.
//
// A plaintext ledger receives neither TLS settings nor a mount. An encrypted
// ledger always receives PGSSLMODE=verify-full. If the certificate directory
// is empty, this helper omits the mount and the caller rejects the
// configuration before starting a container. The explicit mode prevents a
// return to the client's unverified default.
func ybDumpTransport(cfg *config.Config) (env []string, binds []string) {
	if !cfg.LedgerTLSEnabled {
		return nil, nil
	}

	certsDir := ledgerCertsDir(cfg)
	if certsDir == "" {
		return []string{"PGSSLMODE=verify-full"}, nil
	}

	env = []string{
		"PGSSLMODE=verify-full",
		"PGSSLROOTCERT=" + certsDir + "/ca.crt",
	}
	binds = []string{certsDir + ":" + certsDir + ":ro"}

	return env, binds
}

// ybDumpAttemptOutcome checks the container result and output file. It
// returns the artifact size or a refusal reason. A clean process exit
// without a nonempty file still fails; a later endpoint may produce the
// required artifact.
func ybDumpAttemptOutcome(
	res execResult,
	runErr error,
	outPath string,
) (size int64, reason string) {
	if runErr != nil {
		return 0, "dump one-shot: " + runErr.Error()
	}

	if res.ExitCode != 0 {
		return 0, fmt.Sprintf(
			"exited %d: %s",
			res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr),
		)
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
