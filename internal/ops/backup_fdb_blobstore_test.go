package ops

import (
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestFDBBlobstoreURL checks the exact blobstore:// destination for
// continuous fdbbackup start with a plain-HTTP SeaweedFS IPv6 endpoint.
// The URL must use fdb-blobstore-host, preserve the port, and include the
// secret verbatim. url.Values.Encode sorts the query keys as bucket, region,
// and secure_connection.
func TestFDBBlobstoreURL(t *testing.T) {
	cfg := &config.Config{
		BackupS3Endpoint:   "http://[3d06:bad:b01:204::410]:8333",
		BackupS3AccessKey:  "AKIAEXAMPLE",
		BackupS3SecretKey:  "secretexample",
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: "tack-backups",
	}
	got, err := fdbBlobstoreURL(cfg, "20260528T000000Z")
	if err != nil {
		t.Fatalf("fdbBlobstoreURL: %v", err)
	}
	want := "blobstore://AKIAEXAMPLE:secretexample@fdb-blobstore-host:8333/20260528T000000Z" +
		"?bucket=tack-backups&region=us-east-1&secure_connection=0"
	if got != want {
		t.Errorf("blobstore url mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestFDBBlobstoreURLStripsHTTPS checks that the encoder removes https://
// as it removes http:// and replaces the IPv6 literal with the synthetic
// hostname.
func TestFDBBlobstoreURLStripsHTTPS(t *testing.T) {
	cfg := &config.Config{
		BackupS3Endpoint:   "https://[3d06:bad:b01:204::410]:8333",
		BackupS3AccessKey:  "key",
		BackupS3SecretKey:  "sec",
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: "tack-backups",
	}
	got, err := fdbBlobstoreURL(cfg, "run")
	if err != nil {
		t.Fatalf("fdbBlobstoreURL: %v", err)
	}
	want := "blobstore://key:sec@fdb-blobstore-host:8333/run" +
		"?bucket=tack-backups&region=us-east-1&secure_connection=0"
	if got != want {
		t.Errorf("blobstore url mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestBlobstoreHostMappingIPv6Literal checks the URL host
// fdb-blobstore-host:8333 and the Docker ExtraHosts entry
// fdb-blobstore-host:3d06:bad:b01:204::410. The mapping uses bare IPv6 without
// brackets. The original QA observation on 2026-05-28 used this exact case.
func TestBlobstoreHostMappingIPv6Literal(t *testing.T) {
	hostForURL, extraHost, err := blobstoreHostMapping("http://[3d06:bad:b01:204::410]:8333")
	if err != nil {
		t.Fatalf("blobstoreHostMapping: %v", err)
	}
	wantHost := "fdb-blobstore-host:8333"
	if hostForURL != wantHost {
		t.Errorf("hostForURL mismatch:\n got=%s\nwant=%s", hostForURL, wantHost)
	}
	wantExtra := "fdb-blobstore-host:3d06:bad:b01:204::410"
	if extraHost != wantExtra {
		t.Errorf("extraHost mismatch:\n got=%s\nwant=%s", extraHost, wantExtra)
	}
}

// TestBlobstoreHostMappingPlainHostname checks that the encoder preserves a
// plain hostname in the URL without an ExtraHosts mapping. fdbbackup resolves
// that hostname directly.
func TestBlobstoreHostMappingPlainHostname(t *testing.T) {
	hostForURL, extraHost, err := blobstoreHostMapping("http://seaweedfs-s3:8333")
	if err != nil {
		t.Fatalf("blobstoreHostMapping: %v", err)
	}
	if hostForURL != "seaweedfs-s3:8333" {
		t.Errorf("hostForURL mismatch:\n got=%s\nwant=seaweedfs-s3:8333", hostForURL)
	}
	if extraHost != "" {
		t.Errorf("extraHost should be empty for a plain hostname, got %q", extraHost)
	}
}

// TestBlobstoreExtraHostsIPv6Literal checks that the helper returns the
// single IPv6 mapping that backup containers require.
func TestBlobstoreExtraHostsIPv6Literal(t *testing.T) {
	got, err := blobstoreExtraHosts("http://[3d06:bad:b01:204::410]:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}
	if len(got) != 1 || got[0] != "fdb-blobstore-host:3d06:bad:b01:204::410" {
		t.Errorf("extra hosts mismatch: got=%v", got)
	}
}

// TestBlobstoreExtraHostsPlainHostname checks that a plain hostname leaves
// the Docker HostConfig ExtraHosts field nil.
func TestBlobstoreExtraHostsPlainHostname(t *testing.T) {
	got, err := blobstoreExtraHosts("http://seaweedfs-s3:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil extra hosts for plain hostname, got %v", got)
	}
}

// TestFDBBlobstoreURLRequiresCredentials checks that the encoder refuses
// missing credentials rather than creating an unauthenticated destination.
func TestFDBBlobstoreURLRequiresCredentials(t *testing.T) {
	cfg := &config.Config{
		BackupS3Endpoint:   "http://[3d06:bad:b01:204::410]:8333",
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: "tack-backups",
	}
	_, err := fdbBlobstoreURL(cfg, "run")
	if err == nil {
		t.Fatalf("expected error when credentials are empty, got nil")
	}
}

// TestFDBBlobstoreURLRejectsEmptyEndpoint checks that the encoder rejects an
// empty endpoint rather than creating a blobstore URL without a host.
func TestFDBBlobstoreURLRejectsEmptyEndpoint(t *testing.T) {
	cfg := &config.Config{
		BackupS3AccessKey:  "key",
		BackupS3SecretKey:  "sec",
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: "tack-backups",
	}
	_, err := fdbBlobstoreURL(cfg, "run")
	if err == nil {
		t.Fatalf("expected error when endpoint is empty, got nil")
	}
}
