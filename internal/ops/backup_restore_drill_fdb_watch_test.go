package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestParseFDBRestoreExitRejectsUnreadableStatus rejects malformed probe output
// rather than treating an unreadable response as a running restore.
func TestParseFDBRestoreExitRejectsUnreadableStatus(t *testing.T) {
	for _, output := range []string{"", "  ", "exited", "exited x", "finished 0", "0"} {
		t.Run(
			output,
			func(t *testing.T) {
				if _, _, err := parseFDBRestoreExit(output); err == nil {
					t.Fatalf("parsing %q as an exit status must fail", output)
				}
			},
		)
	}
}

// TestParseFDBRestoreExitReadsTheProbe checks running and exited probe responses.
func TestParseFDBRestoreExitReadsTheProbe(t *testing.T) {
	if code, restoreFinished, err := parseFDBRestoreExit(
		"running\n",
	); err != nil || restoreFinished ||
		code != 0 {
		t.Fatalf("running probe = (%d, %v, %v), want (0, false, nil)", code, restoreFinished, err)
	}

	if code, restoreFinished, err := parseFDBRestoreExit(
		"exited 3\n",
	); err != nil || !restoreFinished ||
		code != 3 {
		t.Fatalf("exited probe = (%d, %v, %v), want (3, true, nil)", code, restoreFinished, err)
	}
}

// fdbRestoreStatusWithURL formats counters and the credential-bearing source URL
// as FoundationDB 7.4.6 fdbrestore status prints them.
func fdbRestoreStatusWithURL(counters string) string {
	return counters + "  URL: " + drillDestURL + "  Range: ''-'\\xff'  AddPrefix: ''  RemovePrefix: ''  Version: 100731044\n"
}

// TestFDBRestoreStatusReadingRedactsCredentials checks successful status output
// and failure diagnostics for credentials, while preserving progress counters.
func TestFDBRestoreStatusReadingRedactsCredentials(t *testing.T) {
	cfg := &config.Config{
		BackupS3AccessKey: "drill-access",
		BackupS3SecretKey: "drill-secret",
	} // gitleaks:allow test placeholder
	counters := fdbStatusLine(7, 900, 4, 28672, 3)

	statusOutput, err := fdbRestoreStatusReading(
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
		if strings.Contains(statusOutput, credential) {
			t.Fatalf("the status output still contains %q: %q", credential, statusOutput)
		}
	}

	if !strings.Contains(statusOutput, "URL: blobstore://") {
		t.Fatalf("redaction must preserve the source URL without credentials: %q", statusOutput)
	}

	progress := newFDBRestoreProgress()
	if !progress.observe(statusOutput) {
		t.Fatalf("the redacted status output must still include its counters: %q", statusOutput)
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
			Stderr:   "ERROR: cannot connect to " + drillDestURL,
			ExitCode: 1,
		},
	)
	if err == nil {
		t.Fatal("a status command with a nonzero exit code must return an error")
	}

	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(err.Error(), credential) {
			t.Fatalf("the status failure still contains %q: %v", credential, err)
		}
	}
}
