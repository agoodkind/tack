package ops

import (
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestFDBBlobstoreURL checks the destination for an HTTP SeaweedFS endpoint
// with an IPv6 literal. The synthetic hostname must retain the port, the URL
// must include the secret verbatim, and query keys must follow the alphabetical
// order from url.Values.Encode.
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

// TestFDBBlobstoreURLStripsHTTPS checks scheme removal and IPv6 hostname
// substitution for an HTTPS endpoint.
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

// TestBlobstoreHostMappingIPv6Literal checks the synthetic URL host and the
// unbracketed address in the Docker ExtraHosts mapping, matching the endpoint
// form verified on QA on 2026-05-28.
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

// TestBlobstoreHostMappingPlainHostname checks that direct hostname resolution
// does not require an ExtraHosts mapping or change the URL authority.
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

// TestBlobstoreExtraHostsIPv6Literal checks the single IPv6 mapping.
func TestBlobstoreExtraHostsIPv6Literal(t *testing.T) {
	got, err := blobstoreExtraHosts("http://[3d06:bad:b01:204::410]:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}

	if len(got) != 1 || got[0] != "fdb-blobstore-host:3d06:bad:b01:204::410" {
		t.Errorf("extra hosts mismatch: got=%v", got)
	}
}

// TestBlobstoreExtraHostsPlainHostname checks that HostConfig can use nil for
// an endpoint that does not require a mapping.
func TestBlobstoreExtraHostsPlainHostname(t *testing.T) {
	got, err := blobstoreExtraHosts("http://seaweedfs-s3:8333")
	if err != nil {
		t.Fatalf("blobstoreExtraHosts: %v", err)
	}

	if got != nil {
		t.Errorf("expected nil extra hosts for plain hostname, got %v", got)
	}
}

// TestFDBBlobstoreURLRequiresCredentials checks that missing credentials cause
// an error instead of an unauthenticated destination URL.
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

// TestFDBBlobstoreURLRejectsEmptyEndpoint checks that an empty endpoint causes
// an error instead of a destination URL without a host.
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
