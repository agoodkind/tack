package ops

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
)

// fdbBlobstoreSyntheticHost replaces IPv6 literal hosts in blobstore URLs.
// fdbbackup fails DNS lookup for describe and hangs on start with IPv6
// literals. Docker ExtraHosts maps this hostname to the literal address.
// QA verified the connection and path-style addressing on 2026-05-28.
const fdbBlobstoreSyntheticHost = "fdb-blobstore-host"

// fdbBlobstoreURL builds a destination for continuous backups in SeaweedFS.
// FoundationDB documents the URL in the Blob Store URLs section:
// https://apple.github.io/foundationdb/backups.html
//
//	blobstore://<key>:<secret>@<host>:<port>/<name>?bucket=<bucket>&region=<region>&secure_connection=0
//
// blobstoreHostMapping removes the endpoint scheme and replaces IPv6 literals
// with fdbBlobstoreSyntheticHost. The caller supplies the corresponding Docker
// ExtraHosts mapping. secure_connection=0 selects HTTP for the SeaweedFS S3
// endpoint on the IPv6-only bridge.
//
// Callers use the run ID as name to address each backup independently.
// This function only encodes the URL; it does not access external resources or
// log messages. Callers log errors, which include endpoint or credential
// context.
func fdbBlobstoreURL(cfg *config.Config, name string) (string, error) {
	hostForURL, _, err := blobstoreHostMapping(cfg.BackupS3Endpoint)
	if err != nil {
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

// blobstoreHostMapping returns the blobstore host and an optional ExtraHosts
// mapping. For [ipv6]:port, it returns fdb-blobstore-host:port and
// fdb-blobstore-host:ipv6, with an unbracketed address in the Docker mapping.
// A plain hostname retains its authority and does not require a mapping.
func blobstoreHostMapping(endpoint string) (hostForURL string, extraHost string, err error) {
	authority, err := stripScheme(endpoint)
	if err != nil {
		return "", "", err
	}

	if !strings.HasPrefix(authority, "[") {
		// fdbbackup resolves plain hostnames directly, with an optional port.
		return authority, "", nil
	}

	closeIdx := strings.LastIndex(authority, "]")
	if closeIdx < 0 {
		return "", "", endpointBracketUnclosedError{Endpoint: endpoint}
	}

	ipv6 := authority[1:closeIdx]
	if ipv6 == "" {
		return "", "", fmt.Errorf("endpoint %q has empty IPv6 literal", endpoint)
	}

	// The optional port follows the closing bracket. Separating it here
	// preserves the colons inside the IPv6 address.
	rest := authority[closeIdx+1:]
	port := ""
	if strings.HasPrefix(rest, ":") {
		port = rest
	} else if rest != "" {
		return "", "", fmt.Errorf(
			"endpoint %q has unexpected trailing data %q after IPv6 literal",
			endpoint,
			rest,
		)
	}

	hostForURL = fdbBlobstoreSyntheticHost + port
	extraHost = fdbBlobstoreSyntheticHost + ":" + ipv6

	return hostForURL, extraHost, nil
}

// stripScheme removes a leading http:// or https:// and returns the authority,
// including brackets around IPv6 literals.
func stripScheme(endpoint string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("endpoint is empty")
	}

	trimmed := strings.TrimPrefix(endpoint, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	if trimmed == "" {
		return "", endpointHostMissingError{Endpoint: endpoint}
	}

	return trimmed, nil
}

// fdbBackupStartArgs builds the command, cluster-file bind, and optional
// ExtraHosts mapping for continuous backup. The destination URL includes the
// secret. The caller logs only the endpoint and bucket, never the URL.
func fdbBackupStartArgs(
	b *backupCtx,
) (cmd []string, binds []string, extraHosts []string, err error) {
	// FoundationDB adds backups/ to the bare run ID when storing object keys.
	dest, err := fdbBlobstoreURL(b.Cfg, b.RunID)
	if err != nil {
		wrapped := fmt.Errorf("build blobstore destination: %w", err)
		b.Log.Error(
			"backup.fdb.blobstore_url_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", wrapped),
		)

		return nil, nil, nil, wrapped
	}

	extraHosts, err = blobstoreExtraHosts(b.Cfg.BackupS3Endpoint)
	if err != nil {
		wrapped := fmt.Errorf("build blobstore extra hosts: %w", err)
		b.Log.Error(
			"backup.fdb.blobstore_extra_hosts_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", wrapped),
		)

		return nil, nil, nil, wrapped
	}

	binds = []string{fdbClusterReadOnlyBind(b.Cfg)}
	// -z (--no-stop-when-done) continues backup after the first restorable
	// snapshot and advances the restore window. Without it fdbbackup stops
	// once the backup becomes restorable.
	cmd = []string{
		"start",
		"-d",
		dest,
		"--snapshot-interval",
		strconv.Itoa(b.Cfg.BackupFDBSnapshotInterval),
		"-z",
	}

	return cmd, binds, extraHosts, nil
}

// blobstoreExtraHosts returns one mapping for an IPv6 literal endpoint or
// nil for a plain hostname. Docker HostConfig uses nil when a mapping is
// unnecessary.
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

type endpointBracketUnclosedError struct {
	Endpoint string
}

func (failure endpointBracketUnclosedError) Error() string {
	return fmt.Sprintf("endpoint %q opens an IPv6 bracket but does not close it", failure.Endpoint)
}

type endpointHostMissingError struct {
	Endpoint string
}

func (failure endpointHostMissingError) Error() string {
	return fmt.Sprintf("endpoint %q does not include a host after the scheme", failure.Endpoint)
}
