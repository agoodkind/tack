package ops

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
)

// fdbBlobstoreSyntheticHost replaces an IPv6 literal host in the blobstore
// URL. The fdbbackup connector cannot resolve that literal: describe returns
// "DNS lookup failed", and start hangs. The container's /etc/hosts entry maps
// the synthetic name to the literal address. Docker ExtraHosts uses the same
// name for this mapping.
//
// The original QA observation on 2026-05-28 reported that fdbbackup connected
// immediately with the /etc/hosts mapping and used path-style addressing.
const fdbBlobstoreSyntheticHost = "fdb-blobstore-host"

// fdbBlobstoreURL builds the blobstore:// destination for fdbbackup start
// when continuous backup writes directly to SeaweedFS. The FoundationDB
// [Blob Store URLs documentation] defines this form:
//
//	blobstore://<key>:<secret>@<host>:<port>/<name>?bucket=<bucket>&region=<region>&secure_connection=0
//
// The function strips http:// or https:// from cfg.BackupS3Endpoint to obtain
// the host and port. For a bracketed IPv6 literal such as
// http://[3d06:bad:b01:204::410]:8333, it substitutes fdb-blobstore-host and
// preserves the port. The caller must supply the matching Docker ExtraHosts
// mapping to the IPv6 address. secure_connection=0 selects plain HTTP for the
// SeaweedFS S3 endpoint on the IPv6-only bridge.
//
// FoundationDB uses name to separate this run's data within the bucket.
// Callers supply the run ID to address each run independently.
//
// The function does not perform I/O or write logs. Callers must log returned
// errors, which include the endpoint or missing-credential context.
//
// [Blob Store URLs documentation]: https://apple.github.io/foundationdb/backups.html
func fdbBlobstoreURL(cfg *config.Config, name string) (string, error) {
	hostForURL, _, err := blobstoreHostMapping(cfg.BackupS3Endpoint)
	if err != nil {
		// blobstoreHostMapping includes the endpoint in its error. Return that error
		// directly; the caller logs errors from this encoder.
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

// blobstoreHostMapping derives the blobstore URL host and any Docker
// ExtraHosts mapping from the configured S3 endpoint.
//
// It removes a leading http:// or https:// before inspecting the authority.
// For a bracketed IPv6 authority ([ipv6]:port), hostForURL becomes
// "fdb-blobstore-host:port" and extraHost becomes "fdb-blobstore-host:ipv6".
// Docker uses the hostname:ip form with the bare IPv6 address. For a plain
// hostname without brackets, hostForURL preserves the authority, and
// extraHost is empty because the container does not need an /etc/hosts entry.
func blobstoreHostMapping(endpoint string) (hostForURL string, extraHost string, err error) {
	authority, err := stripScheme(endpoint)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(authority, "[") {
		// fdbbackup resolves a plain hostname, with an optional port, directly.
		// This endpoint does not need a synthetic name or an ExtraHosts mapping.
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
	// Read the optional :port after the closing bracket. The bracket separates
	// the IPv6 address's colons from the port and preserves the port on the
	// synthetic hostname.
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

// stripScheme removes a leading http:// or https:// from endpoint and
// returns the authority. It preserves IPv6 brackets because they belong to
// the authority.
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

// fdbBackupStartArgs builds the continuous fdbbackup start command with its
// host binds and Docker ExtraHosts entries. It binds only the cluster file.
// An IPv6 object-store endpoint requires one synthetic hostname mapping.
//
// The destination URL includes the blobstore secret. The caller logs only the
// endpoint and bucket; it must not log that URL.
func fdbBackupStartArgs(b *backupCtx) (cmd []string, binds []string, extraHosts []string, err error) {
	// The run ID supplies the bare backup name. FoundationDB stores the backup
	// under backups/<run-id>/ in the bucket and adds its own backups/ prefix.
	dest, err := fdbBlobstoreURL(b.Cfg, b.RunID)
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
	binds = []string{fdbClusterReadOnlyBind(b.Cfg)}
	// The -z (--no-stop-when-done) flag continues the backup after it becomes
	// restorable and advances its restore window. Without the flag, fdbbackup
	// stops when the first snapshot becomes restorable.
	cmd = []string{
		"start",
		"-d", dest,
		"--snapshot-interval", strconv.Itoa(b.Cfg.BackupFDBSnapshotInterval),
		"-z",
	}
	return cmd, binds, extraHosts, nil
}

// blobstoreExtraHosts returns the mappings that backup containers need to
// resolve the endpoint's blobstore host. A plain hostname returns nil and
// leaves the Docker HostConfig ExtraHosts field nil. An IPv6 literal returns
// one synthetic hostname-to-IPv6 mapping.
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
