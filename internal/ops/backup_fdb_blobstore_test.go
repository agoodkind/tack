package ops

import (
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestFDBBlobstoreURL locks down the exact blobstore:// destination the
// continuous FDB backup path passes to `fdbbackup start` for a non-TLS
// SeaweedFS endpoint addressed by an IPv6 literal. The brackets around the
// IPv6 host must survive scheme stripping, the secret must be embedded
// verbatim, and the query keys must be ordered bucket, region,
// secure_connection (the alphabetical order url.Values.Encode produces).
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
	want := "blobstore://AKIAEXAMPLE:secretexample@[3d06:bad:b01:204::410]:8333/20260528T000000Z" +
		"?bucket=tack-backups&region=us-east-1&secure_connection=0"
	if got != want {
		t.Errorf("blobstore url mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestFDBBlobstoreURLStripsHTTPS confirms an https:// endpoint loses its
// scheme the same way http:// does, leaving the bracketed authority intact.
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
	want := "blobstore://key:sec@[3d06:bad:b01:204::410]:8333/run" +
		"?bucket=tack-backups&region=us-east-1&secure_connection=0"
	if got != want {
		t.Errorf("blobstore url mismatch:\n got=%s\nwant=%s", got, want)
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
