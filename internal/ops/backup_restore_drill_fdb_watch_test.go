package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestParseFDBRestoreExitRejectsWhatItCannotRead checks that an unreadable
// completion probe fails instead of counting as a running restore.
func TestParseFDBRestoreExitRejectsWhatItCannotRead(t *testing.T) {
	for _, out := range []string{"", "  ", "exited", "exited x", "finished 0", "0"} {
		t.Run(
			out,
			func(t *testing.T) {
				if _, _, err := parseFDBRestoreExit(out); err == nil {
					t.Fatalf("parsing %q as an exit status must fail", out)
				}
			},
		)
	}
}

// TestParseFDBRestoreExitReadsTheProbe checks both valid probe results.
func TestParseFDBRestoreExitReadsTheProbe(t *testing.T) {
	if code, done, err := parseFDBRestoreExit("running\n"); err != nil || done || code != 0 {
		t.Fatalf("running probe = (%d, %v, %v), want (0, false, nil)", code, done, err)
	}

	if code, done, err := parseFDBRestoreExit("exited 3\n"); err != nil || !done || code != 3 {
		t.Fatalf("exited probe = (%d, %v, %v), want (3, true, nil)", code, done, err)
	}
}

// fdbRestoreStatusWithURL renders one tag's FoundationDB 7.4.6 status line:
// progress counters followed by "URL: <source container>". The source is the
// restore's blobstore URL, including credentials.
func fdbRestoreStatusWithURL(counters string) string {
	return counters + "  URL: " + drillDestURL + "  Range: ''-'\\xff'  AddPrefix: ''  RemovePrefix: ''  Version: 100731044\n"
}

// TestFDBRestoreStatusReadingCarriesNoCredentials checks the credential
// leak in restore status. The source-container URL includes the blobstore
// access key and secret. Both success and failure readings must remove those
// credentials before later logging or error reporting. Redaction must preserve
// the progress counters that the watch reads.
func TestFDBRestoreStatusReadingCarriesNoCredentials(t *testing.T) {
	cfg := &config.Config{
		BackupS3AccessKey: "drill-access",
		BackupS3SecretKey: "drill-secret",
	} // gitleaks:allow test placeholder
	counters := fdbStatusLine(7, 900, 4, 28672, 3)

	reading, err := fdbRestoreStatusReading(
		cfg,
		execResult{
			Stdout:   fdbRestoreStatusWithURL(counters),
			Stderr:   "",
			ExitCode: 0,
		},
	)
	if err != nil {
		t.Fatalf("fdbRestoreStatusReading: %v", err)
	}

	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(reading, credential) {
			t.Fatalf("the status reading still carries %q: %q", credential, reading)
		}
	}

	if !strings.Contains(reading, "URL: blobstore://") {
		t.Fatalf("redaction must remove the credentials, not the reading: %q", reading)
	}

	progress := newFDBRestoreProgress()
	if !progress.observe(reading) {
		t.Fatalf("the redacted reading must still carry its counters: %q", reading)
	}

	for _, mark := range []string{
		"blocks=7",
		"blockstotal=900",
		"byteswritten=28672",
		"applyversionlag=3",
	} {
		if !strings.Contains(progress.summary(), mark) {
			t.Fatalf("counter %s did not survive the redaction, got %q", mark, progress.summary())
		}
	}

	_, err = fdbRestoreStatusReading(
		cfg,
		execResult{
			Stdout:   "",
			Stderr:   "ERROR: cannot reach " + drillDestURL,
			ExitCode: 1,
		},
	)
	if err == nil {
		t.Fatal("a status that exited non-zero must be an error")
	}

	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(err.Error(), credential) {
			t.Fatalf("the status failure still carries %q: %v", credential, err)
		}
	}
}
