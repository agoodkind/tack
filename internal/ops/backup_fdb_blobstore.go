package ops

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
)

// fdbBlobstoreURL builds the `blobstore://` destination URL that
// `fdbbackup start` writes to when continuous backup streams straight to the
// SeaweedFS object store. The URL form is documented at
// https://apple.github.io/foundationdb/backups.html under the Blob Store URLs
// section:
//
//	blobstore://<key>:<secret>@<host>:<port>/<name>?bucket=<bucket>&region=<region>&secure_connection=0
//
// The host:port pair is derived from cfg.BackupS3Endpoint by stripping the
// http:// or https:// scheme. The SeaweedFS endpoint is an IPv6 literal in
// brackets (for example http://[3d06:bad:b01:204::410]:8333), so the brackets
// are preserved verbatim. secure_connection=0 selects plain HTTP because the
// SeaweedFS S3 endpoint is non-TLS on the IPv6-only bridge.
//
// name is the backup name FoundationDB uses to namespace this run's data
// inside the bucket; callers pass the run ID so each run is independently
// addressable.
//
// fdbBlobstoreURL is a pure encoder: it touches no I/O and logs nothing, so
// the caller is responsible for logging when it returns an error. The returned
// error already carries the endpoint or credential context the caller needs.
func fdbBlobstoreURL(cfg *config.Config, name string) (string, error) {
	hostPort, err := stripScheme(cfg.BackupS3Endpoint)
	if err != nil {
		// stripScheme already includes the endpoint in its message, so the
		// error is returned directly rather than re-wrapped; this keeps the
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
		hostPort,
		name,
		query.Encode(),
	)
	return dest, nil
}

// stripScheme removes a leading http:// or https:// from endpoint and returns
// the bare host:port authority. The IPv6 literal stays bracketed because the
// brackets are part of the authority, not the scheme.
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

// fdbBackupStartArgs builds the fdbbackup start command and host binds for the
// run. The continuous and one-shot paths differ in destination URL, the
// presence of `-w`, and the `--snapshot_interval` flag. The blobstore secret is
// embedded in the destination URL and never logged; only the endpoint and
// bucket are logged at the call site. See
// https://apple.github.io/foundationdb/backups.html
func fdbBackupStartArgs(b *backupCtx) ([]string, []string, error) {
	binds := []string{
		"/etc/foundationdb:/etc/foundationdb:ro",
		b.SnapshotDir + ":/snapshot",
	}
	if !b.Cfg.BackupFDBContinuous {
		cmd := []string{"start", "-w", "-d", "file:///snapshot/" + b.RunID}
		return cmd, binds, nil
	}
	dest, err := fdbBlobstoreURL(b.Cfg, b.RunID)
	if err != nil {
		wrapped := fmt.Errorf("build blobstore destination: %w", err)
		b.Log.Error("backup.fdb.blobstore_url_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", wrapped),
		)
		return nil, nil, wrapped
	}
	cmd := []string{
		"start",
		"-d", dest,
		"--snapshot_interval", strconv.Itoa(b.Cfg.BackupFDBSnapshotInterval),
	}
	return cmd, binds, nil
}
