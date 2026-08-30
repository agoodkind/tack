package ops

import "testing"

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
