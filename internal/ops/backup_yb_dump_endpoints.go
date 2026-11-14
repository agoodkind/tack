// SQL exports try configured ledger endpoints in order until one produces a
// usable artifact. A single unavailable node must not prevent backups while
// the remaining cluster serves reads and writes.

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

// ybDumpHosts extracts unique YSQL hosts in master_addresses order, without
// RPC ports. These configured cluster members also serve YSQL.
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

// ybDumpEndpointRefusal records an endpoint and its failure for the final error.
type ybDumpEndpointRefusal struct {
	host   string
	reason string
}

// runYBDumpOneShot tries configured endpoints until one writes the artifact.
func runYBDumpOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	spec ybDumpSpec,
) error {
	// TLS without a certificate authority directory cannot verify the node.
	// Reject it before container creation rather than accepting the client
	// library's unverified default connection mode (TACK-460).
	if cfg.LedgerTLSEnabled && ledgerCertsDir(cfg) == "" {
		err := fmt.Errorf(
			"ysql %s dump: TLS node verification requires TACK_LEDGER_CERTS_DIR, which is empty",
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

// walkYBDumpEndpoints logs each refusal, including refusals before a later
// endpoint succeeds. Exhaustion fails with every attempted host and reason.
// The caller must not upload a run without the complete SQL artifact.
func walkYBDumpEndpoints(
	ctx context.Context,
	hosts []string,
	spec ybDumpSpec,
	dump func(host string) (size int64, reason string),
) error {
	logger := telemetry.L(ctx)
	if len(hosts) == 0 {
		wrapped := fmt.Errorf(
			"ysql %s dump: the configured master addresses do not specify an endpoint",
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
			"ysql %s dump: every endpoint failed; tried %s: %s",
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

// dumpYBFromEndpoint runs a dump container with the staging bind mount and
// returns the artifact size or refusal reason. Empty artifacts count as
// failures because they cannot restore the expected schema or roles.
// Every refusal permits the next endpoint without interpreting localized
// dumper error text, even when another attempt may repeat the failure.
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

// ybDumpTransport supplies libpq TLS settings and a read-only certificate
// directory bind (TACK-460). Plaintext connections do not require either.
// TLS always selects verify-full, even when the caller must reject a missing
// directory before launch. The library must not select its unverified default.
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

// ybDumpAttemptOutcome checks container execution and the expected output file.
// A successful process without a nonempty artifact still fails this attempt.
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
