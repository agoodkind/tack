package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestParseFDBRestoreExitRejectsWhatItCannotRead proves the probe that tells
// the wait a restore finished never guesses. An answer nobody could read must
// not pass for a restore that is still running.
func TestParseFDBRestoreExitRejectsWhatItCannotRead(t *testing.T) {
	for _, out := range []string{"", "  ", "exited", "exited x", "finished 0", "0"} {
		t.Run(out, func(t *testing.T) {
			if _, _, err := parseFDBRestoreExit(out); err == nil {
				t.Fatalf("parsing %q as an exit status must fail", out)
			}
		})
	}
}

// TestParseFDBRestoreExitReadsTheProbe locks the two answers the probe gives.
func TestParseFDBRestoreExitReadsTheProbe(t *testing.T) {
	if code, done, err := parseFDBRestoreExit("running\n"); err != nil || done || code != 0 {
		t.Fatalf("running probe = (%d, %v, %v), want (0, false, nil)", code, done, err)
	}
	if code, done, err := parseFDBRestoreExit("exited 3\n"); err != nil || !done || code != 3 {
		t.Fatalf("exited probe = (%d, %v, %v), want (3, true, nil)", code, done, err)
	}
}

// fdbRestoreStatusWithURL is one tag's line the way foundationdb 7.4.6's
// `fdbrestore status` prints it: the progress counters, then "URL: <source
// container>", which is the blobstore URL the restore was started with,
// credentials included.
func fdbRestoreStatusWithURL(counters string) string {
	return counters + "  URL: " + drillDestURL + "  Range: ''-'\\xff'  AddPrefix: ''  RemovePrefix: ''  Version: 100731044\n"
}

// TestFDBRestoreStatusReadingCarriesNoCredentials is the leak this test
// exists for. The status the watch reads names the source container URL, and
// that URL carries the blobstore access key and secret. The reading has to come
// out of the exec with both gone, on the success path as well as the failure
// path, so nothing that later carries the reading into a log or an error can
// write them; and the counters the watch reads have to survive the redaction.
func TestFDBRestoreStatusReadingCarriesNoCredentials(t *testing.T) {
	cfg := &config.Config{BackupS3AccessKey: "drill-access", BackupS3SecretKey: "drill-secret"} // gitleaks:allow test placeholder
	counters := fdbStatusLine(7, 900, 4, 28672, 3)

	reading, err := fdbRestoreStatusReading(cfg, execResult{
		Stdout:   fdbRestoreStatusWithURL(counters),
		Stderr:   "",
		ExitCode: 0,
	})
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
	for _, mark := range []string{"blocks=7", "blockstotal=900", "byteswritten=28672", "applyversionlag=3"} {
		if !strings.Contains(progress.summary(), mark) {
			t.Fatalf("counter %s did not survive the redaction, got %q", mark, progress.summary())
		}
	}

	_, err = fdbRestoreStatusReading(cfg, execResult{
		Stdout:   "",
		Stderr:   "ERROR: cannot reach " + drillDestURL,
		ExitCode: 1,
	})
	if err == nil {
		t.Fatal("a status that exited non-zero must be an error")
	}
	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(err.Error(), credential) {
			t.Fatalf("the status failure still carries %q: %v", credential, err)
		}
	}
}
