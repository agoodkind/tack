package ops

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
)

// fdbBlobstoreSyntheticHost is the fixed hostname substituted into the
// blobstore URL when the configured endpoint is an IPv6 literal. fdbbackup's
// blobstore connector cannot resolve an IPv6 literal host (it returns "DNS
// lookup failed" for describe and hangs on start), but it resolves a plain
// hostname fine when the container has an /etc/hosts entry mapping that name
// to the literal address. The Docker ExtraHosts entry that carries that
// mapping uses this same name. Proven on QA 2026-05-28: with an /etc/hosts
// mapping fdbbackup connected instantly and used path-style addressing.
const fdbBlobstoreSyntheticHost = "fdb-blobstore-host"

// fdbBlobstoreURL builds the `blobstore://` destination URL that
// `fdbbackup start` writes to when continuous backup streams straight to the
// SeaweedFS object store. The URL form is documented at
// https://apple.github.io/foundationdb/backups.html under the Blob Store URLs
// section:
//
//	blobstore://<key>:<secret>@<host>:<port>/<name>?bucket=<bucket>&region=<region>&secure_connection=0
//
// The host:port pair is derived from cfg.BackupS3Endpoint by stripping the
// http:// or https:// scheme. When the endpoint is an IPv6 literal in brackets
// (for example http://[3d06:bad:b01:204::410]:8333) the bracketed authority is
// replaced with the synthetic hostname fdb-blobstore-host because fdbbackup
// cannot resolve an IPv6 literal; the caller pairs this with a matching Docker
// ExtraHosts entry so the container's /etc/hosts resolves the name back to the
// literal. secure_connection=0 selects plain HTTP because the SeaweedFS S3
// endpoint is non-TLS on the IPv6-only bridge.
//
// name is the backup name FoundationDB uses to namespace this run's data
// inside the bucket; callers pass the run ID so each run is independently
// addressable.
//
// fdbBlobstoreURL is a pure encoder: it touches no I/O and logs nothing, so
// the caller is responsible for logging when it returns an error. The returned
// error already carries the endpoint or credential context the caller needs.
func fdbBlobstoreURL(cfg *config.Config, name string) (string, error) {
	hostForURL, _, err := blobstoreHostMapping(cfg.BackupS3Endpoint)
	if err != nil {
		// blobstoreHostMapping already includes the endpoint in its message, so
		// the error is returned directly rather than re-wrapped; this keeps the
		// pure-encoder shape free of a logging obligation.
		return "", err
	}
	if cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		return "", fmt.Errorf("blobstore destination requires S3 access key and secret key")
	}

	query := url.Values{}
	query.Set("bucket", cfg.BackupS3BucketMain)
	query.Set("region", cfg.BackupS3Region)
	query.Set("secure_connection", "0")

	dest := fmt.Sprintf(
		"blobstore://%s:%s@%s/%s?%s",
		cfg.BackupS3AccessKey,
		cfg.BackupS3SecretKey,
		hostForURL,
		name,
		query.Encode(),
	)
	return dest, nil
}

// blobstoreHostMapping resolves the configured S3 endpoint into the host the
// blobstore URL must use and, when needed, the Docker ExtraHosts entry that
// makes that host resolvable inside the backup containers.
//
// It strips a leading http:// or https:// from endpoint, then inspects the
// authority. When the authority is an IPv6 literal in brackets ([ipv6]:port),
// hostForURL becomes "fdb-blobstore-host:port" and extraHost becomes
// "fdb-blobstore-host:ipv6" (the Docker ExtraHosts "hostname:ip" form, with
// the IPv6 address bare and without brackets). When the authority is already a
// plain hostname (no brackets), hostForURL is the authority unchanged and
// extraHost is "" because no /etc/hosts mapping is needed.
func blobstoreHostMapping(endpoint string) (hostForURL string, extraHost string, err error) {
	authority, err := stripScheme(endpoint)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(authority, "[") {
		// Plain hostname (optionally host:port). fdbbackup resolves it directly,
		// so no synthetic name and no ExtraHosts mapping are required.
		return authority, "", nil
	}
	closeIdx := strings.LastIndex(authority, "]")
	if closeIdx < 0 {
		return "", "", fmt.Errorf("endpoint %q has an opening bracket but no closing bracket", endpoint)
	}
	ipv6 := authority[1:closeIdx]
	if ipv6 == "" {
		return "", "", fmt.Errorf("endpoint %q has empty IPv6 literal", endpoint)
	}
	// Everything after the closing bracket is the optional :port. Split on the
	// first ":" after "]" so the port stays attached to the synthetic host and
	// the IPv6 literal (which itself contains colons) is not mis-split.
	rest := authority[closeIdx+1:]
	port := ""
	if strings.HasPrefix(rest, ":") {
		port = rest
	} else if rest != "" {
		return "", "", fmt.Errorf("endpoint %q has unexpected trailing data %q after IPv6 literal", endpoint, rest)
	}
	hostForURL = fdbBlobstoreSyntheticHost + port
	extraHost = fdbBlobstoreSyntheticHost + ":" + ipv6
	return hostForURL, extraHost, nil
}

// stripScheme removes a leading http:// or https:// from endpoint and returns
// the bare authority. The IPv6 literal stays bracketed because the brackets
// are part of the authority, not the scheme.
func stripScheme(endpoint string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("endpoint is empty")
	}
	trimmed := strings.TrimPrefix(endpoint, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	if trimmed == "" {
		return "", fmt.Errorf("endpoint %q has no host after scheme", endpoint)
	}
	return trimmed, nil
}

// fdbBackupStartArgs builds the fdbbackup start command, host binds, and Docker
// ExtraHosts for the run. The continuous and one-shot paths differ in
// destination URL, the presence of `-w`, the `--snapshot_interval` flag, and
// the binds. The one-shot file:// path binds the local snapshot directory and
// needs no ExtraHosts. The continuous blobstore path binds only the cluster
// file and returns one ExtraHosts entry (the synthetic hostname-to-IPv6
// mapping) so the fdbbackup container resolves the blobstore host. The blobstore
// secret is embedded in the destination URL and never logged; only the endpoint
// and bucket are logged at the call site. See
// https://apple.github.io/foundationdb/backups.html
func fdbBackupStartArgs(b *backupCtx) (cmd []string, binds []string, extraHosts []string, err error) {
	if !b.Cfg.BackupFDBContinuous {
		binds = []string{
			"/etc/foundationdb:/etc/foundationdb:ro",
			b.SnapshotDir + ":/snapshot",
		}
		cmd = []string{"start", "-w", "-d", "file:///snapshot/" + b.RunID}
		return cmd, binds, nil, nil
	}
	// The backup name is prefixed with backups/ so the object keys land under the
	// backups/ folder that the restore drill lists to discover the latest backup.
	dest, err := fdbBlobstoreURL(b.Cfg, "backups/"+b.RunID)
	if err != nil {
		wrapped := fmt.Errorf("build blobstore destination: %w", err)
		b.Log.Error("backup.fdb.blobstore_url_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", wrapped),
		)
		return nil, nil, nil, wrapped
	}
	extraHosts, err = blobstoreExtraHosts(b.Cfg.BackupS3Endpoint)
	if err != nil {
		wrapped := fmt.Errorf("build blobstore extra hosts: %w", err)
		b.Log.Error("backup.fdb.blobstore_extra_hosts_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", wrapped),
		)
		return nil, nil, nil, wrapped
	}
	binds = []string{"/etc/foundationdb:/etc/foundationdb:ro"}
	cmd = []string{
		"start",
		"-d", dest,
		"--snapshot_interval", strconv.Itoa(b.Cfg.BackupFDBSnapshotInterval),
	}
	return cmd, binds, extraHosts, nil
}

// blobstoreExtraHosts returns the Docker ExtraHosts entries the backup
// containers need to resolve the blobstore host derived from endpoint. It
// returns nil when the endpoint is a plain hostname (no mapping required) and a
// single-element slice with the synthetic hostname-to-IPv6 mapping when the
// endpoint is an IPv6 literal. The empty-vs-populated split keeps the Docker
// HostConfig field nil when there is nothing to map.
func blobstoreExtraHosts(endpoint string) ([]string, error) {
	_, extraHost, err := blobstoreHostMapping(endpoint)
	if err != nil {
		return nil, err
	}
	if extraHost == "" {
		return nil, nil
	}
	return []string{extraHost}, nil
}
