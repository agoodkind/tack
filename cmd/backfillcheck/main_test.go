package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const backfillCheckToday = "2026-09-19"

func chdirToCommandFile(t *testing.T, name string, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Chdir(dir)
}

func backfillCheckDay(t *testing.T) time.Time {
	t.Helper()
	day, err := time.Parse(time.DateOnly, backfillCheckToday)
	if err != nil {
		t.Fatalf("parse today: %v", err)
	}
	return day
}

func TestExpiredBackfillFailsTheBuild(t *testing.T) {
	chdirToCommandFile(t, "cli_backfill.go", `package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
)

var lifetime = clispec.Lifetime{Ticket: "TACK-461", RemoveBy: time.Date(2026, time.September, 18, 0, 0, 0, 0, time.UTC)}
`)
	var out bytes.Buffer

	code := run(backfillCheckDay(t), &out)

	if code != 1 {
		t.Fatalf("exit = %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "backfill for TACK-461 was to be deleted by 2026-09-18") {
		t.Fatalf("output lacks the expired backfill:\n%s", out.String())
	}
}

func TestBackfillBeforeItsRemovalDayPasses(t *testing.T) {
	chdirToCommandFile(t, "cli_backfill.go", `package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
)

var lifetime = clispec.Lifetime{Ticket: "TACK-461", RemoveBy: time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC)}
`)
	var out bytes.Buffer

	if code := run(backfillCheckDay(t), &out); code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
}

func TestBackfillWithoutALiteralDayFailsTheBuild(t *testing.T) {
	chdirToCommandFile(t, "cli_backfill.go", `package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
)

var removeBy = time.Now()

var lifetime = clispec.Lifetime{Ticket: "TACK-461", RemoveBy: removeBy}
`)
	var out bytes.Buffer

	if code := run(backfillCheckDay(t), &out); code != 1 {
		t.Fatalf("exit = %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "must be a time.Date literal") {
		t.Fatalf("output lacks the literal requirement:\n%s", out.String())
	}
}

func TestTestFilesAreNotScanned(t *testing.T) {
	chdirToCommandFile(t, "cli_backfill_test.go", `package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
)

var lifetime = clispec.Lifetime{Ticket: "TACK-461", RemoveBy: time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)}
`)
	var out bytes.Buffer

	if code := run(backfillCheckDay(t), &out); code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
}
