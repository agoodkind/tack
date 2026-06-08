package ops

import (
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestFDBBlobstoreURL locks down the exact blobstore:// destination the
// continuous FDB backup path passes to `fdbbackup start` for a non-TLS
// SeaweedFS endpoint addressed by an IPv6 literal. fdbbackup cannot resolve an
// IPv6 literal, so the bracketed authority is replaced with the synthetic host
// fdb-blobstore-host (port preserved); the secret must be embedded verbatim,
// and the query keys must be ordered bucket, region, secure_connection (the
// alphabetical order url.Values.Encode produces).
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

// TestFDBBlobstoreURLStripsHTTPS confirms an https:// endpoint loses its
// scheme the same way http:// does and that the IPv6 literal is substituted
// with the synthetic host.
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

// TestBlobstoreHostMappingIPv6Literal verifies the IPv6-literal endpoint
// resolves to the synthetic URL host fdb-blobstore-host:8333 and the Docker
// ExtraHosts entry fdb-blobstore-host:3d06:bad:b01:204::410 (bare IPv6, no
// brackets). This is the exact case proven on QA 2026-05-28.
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

// TestBlobstoreHostMappingPlainHostname verifies a plain-hostname endpoint is
// returned unchanged for the URL and produces no ExtraHosts mapping, since
// fdbbackup can resolve the name directly.
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

// TestBlobstoreExtraHostsIPv6Literal confirms the slice helper wraps the
// single IPv6 mapping the backup containers need.
func TestBlobstoreExtraHostsIPv6Literal(t *testing.T) {
	got, err := blobstoreExtraHosts("http://[3d06:bad:b01:204::410]:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}
	if len(got) != 1 || got[0] != "fdb-blobstore-host:3d06:bad:b01:204::410" {
		t.Errorf("extra hosts mismatch: got=%v", got)
	}
}

// TestBlobstoreExtraHostsPlainHostname confirms a plain hostname yields no
// ExtraHosts so the Docker HostConfig field stays nil.
func TestBlobstoreExtraHostsPlainHostname(t *testing.T) {
	got, err := blobstoreExtraHosts("http://seaweedfs-s3:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil extra hosts for plain hostname, got %v", got)
	}
}

// TestFDBBlobstoreURLRequiresCredentials confirms the helper refuses to build
// a destination when either credential is missing, so a misconfigured run
// fails loudly instead of writing an unauthenticated URL.
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

// TestFDBBlobstoreURLRejectsEmptyEndpoint confirms an empty endpoint is a hard
// error rather than producing a host-less blobstore URL.
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
