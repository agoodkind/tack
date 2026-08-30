package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// fdbStatusRestorable is `fdbbackup status` output captured verbatim from a
// live foundationdb 7.4.6 continuous backup on 2026-08-30, the steady state the
// production session runs in.
const fdbStatusRestorable = "The backup on tag `default' is restorable but continuing to file:///var/fdb/backups/backup-2026-08-30-01-05-43.994468.\n" +
	"BackupUID: 32d0e1787333ae5acb80042744bc3bf1\n" +
	"BackupURL: file:///var/fdb/backups/backup-2026-08-30-01-05-43.994468\n" +
	"Snapshot interval is 60 seconds.  Current snapshot progress target is 80.60% (>100% means the snapshot is supposed to be done)\n" +
	"\n" +
	"Details:\n" +
	" LogBytes written - 0\n" +
	" RangeBytes written - 413\n" +
	" Last complete log version and timestamp        - 100720665, 2026/08/30.01:07:23+0000\n" +
	" Last complete snapshot version and timestamp   - 56869908, 2026/08/30.01:06:39+0000\n" +
	" Current Snapshot start version and timestamp   - 62454655, 2026/08/30.01:06:46+0000\n" +
	" Expected snapshot end version and timestamp    - 122454655, 2026/08/30.01:07:45+0000\n" +
	" Backup supposed to stop at next snapshot completion - No\n"

// fdbStatusNotYetRestorable is the same command's output captured from the same
// session before its first snapshot finished, when no restore is possible yet.
const fdbStatusNotYetRestorable = "The backup on tag `default' is in progress to file:///var/fdb/backups/backup-2026-08-30-01-05-43.994468.\n" +
	"BackupUID: 32d0e1787333ae5acb80042744bc3bf1\n" +
	"BackupURL: file:///var/fdb/backups/backup-2026-08-30-01-05-43.994468\n" +
	"Snapshot interval is 60 seconds.  The initial snapshot is still running.\n" +
	"\n" +
	"Details:\n" +
	" LogBytes written - 0\n" +
	" RangeBytes written - 174\n" +
	" Last complete log version and timestamp        - 720665, 2026/08/30.01:05:43+0000\n" +
	" Backup supposed to stop at next snapshot completion - No\n"

// ybHealthAllGood is the /api/v1/health-check body captured verbatim from
// yugabytedb/yugabyte:2025.2.3.0-b149, the image the backup family pins.
const ybHealthAllGood = `{"dead_nodes":[],"most_recent_uptime":0,"under_replicated_tablets":[]}`

// TestBackupStalenessClassification covers the verdict every alert acts on: a
// mechanism inside its window is fresh, one exactly at the threshold is still
// fresh, and one past it is stale.
func TestBackupStalenessClassification(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	threshold := 36 * time.Hour
	tests := []struct {
		name      string
		at        time.Time
		wantStale bool
	}{
		{name: "fresh", at: now.Add(-3 * time.Hour), wantStale: false},
		{name: "at threshold", at: now.Add(-threshold), wantStale: false},
		{name: "one second past threshold", at: now.Add(-threshold - time.Second), wantStale: true},
		{name: "long past threshold", at: now.Add(-30 * 24 * time.Hour), wantStale: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metric := knownBackupStalenessMetric("ledger-export", now, test.at, threshold, "run")
			if metric.stale() != test.wantStale {
				t.Fatalf("stale() = %v for age %s against threshold %s, want %v",
					metric.stale(), metric.Age, threshold, test.wantStale)
			}
			wantVerdict := "FRESH"
			if test.wantStale {
				wantVerdict = "STALE"
			}
			if metric.verdict() != wantVerdict {
				t.Fatalf("verdict() = %q, want %q", metric.verdict(), wantVerdict)
			}
		})
	}
}

// TestUnknownAgeIsStale locks the rule the whole alert rests on: a mechanism
// with no datable success is stale, not fresh. A silently empty backup is the
// incident this check exists to catch, so an absent marker must never read as
// a success.
func TestUnknownAgeIsStale(t *testing.T) {
	metric := unknownBackupStalenessMetric("rehearsal", 8*24*time.Hour, "no marker in tack-backups")
	if !metric.stale() {
		t.Fatal("a metric with no known age must be stale")
	}
	if metric.verdict() != "STALE" {
		t.Fatalf("verdict() = %q, want STALE", metric.verdict())
	}
	if got := backupStalenessAgeField(metric); got != "unknown" {
		t.Fatalf("age field = %q, want unknown", got)
	}
}

// TestBackupStalenessAgeFloorsAtZero proves a success timestamp from a host
// whose clock runs ahead reads as brand new rather than as a negative age.
func TestBackupStalenessAgeFloorsAtZero(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if got := backupStalenessAge(now, now.Add(-90*time.Second)); got != 90*time.Second {
		t.Fatalf("age = %s, want 90s", got)
	}
	if got := backupStalenessAge(now, now.Add(5*time.Minute)); got != 0 {
		t.Fatalf("age of a future timestamp = %s, want 0", got)
	}
}

// TestBackupStalenessReport pins the text the alert mail carries: one line per
// mechanism naming its age, threshold, verdict, and provenance.
func TestBackupStalenessReport(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	report := backupStalenessReport([]backupStalenessMetric{
		knownBackupStalenessMetric(backupStalenessExportName, now, now.Add(-2*time.Hour),
			36*time.Hour, "newest complete run 20260829T100000Z"),
		unknownBackupStalenessMetric(backupStalenessRehearsalName, 8*24*time.Hour,
			"no backup-status/rehearsal.json in tack-backups"),
	})
	want := "ledger-export age=7200s threshold=129600s FRESH newest complete run 20260829T100000Z\n" +
		"rehearsal     age=unknown threshold=691200s STALE no backup-status/rehearsal.json in tack-backups\n"
	if report != want {
		t.Fatalf("report mismatch:\n got=%q\nwant=%q", report, want)
	}
}

// TestBackupStalenessReportKeepsOneLinePerMetric proves a detail carrying an
// error string with newlines cannot split a metric across lines, because the
// alert mail is read one line per mechanism.
func TestBackupStalenessReportKeepsOneLinePerMetric(t *testing.T) {
	report := backupStalenessReport([]backupStalenessMetric{
		unknownBackupStalenessMetric(backupStalenessExportName, time.Hour,
			"listing export runs failed: operation error S3: ListObjectsV2,\n\texceeded maximum number of attempts"),
	})
	if strings.Count(report, "\n") != 1 {
		t.Fatalf("report must be one line:\n%q", report)
	}
	if !strings.HasSuffix(report, "exceeded maximum number of attempts\n") {
		t.Fatalf("report lost the detail:\n%q", report)
	}
}

// TestStaleBackupStalenessMetrics names only the mechanisms past threshold, in
// report order, because that list becomes the command's error.
func TestStaleBackupStalenessMetrics(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	stale := staleBackupStalenessMetrics([]backupStalenessMetric{
		knownBackupStalenessMetric("a", now, now.Add(-time.Hour), 2*time.Hour, ""),
		unknownBackupStalenessMetric("b", time.Hour, ""),
		knownBackupStalenessMetric("c", now, now.Add(-3*time.Hour), 2*time.Hour, ""),
	})
	if strings.Join(stale, ",") != "b,c" {
		t.Fatalf("stale = %v, want [b c]", stale)
	}
}

// TestExportRunKeyIsDatable proves the claim that lets the ledger export skip a
// marker: a run key the orchestrator formats parses back to the instant it was
// generated, so the newest complete run's key is its success timestamp.
func TestExportRunKeyIsDatable(t *testing.T) {
	generated := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	runID := generated.Format(ybSnapshotRunIDLayout)
	if runID != "20260829T010203Z" {
		t.Fatalf("run key = %q, want 20260829T010203Z", runID)
	}
	if !ybRunIDPattern.MatchString(runID) {
		t.Fatalf("run key %q does not match the manifest's run-id pattern", runID)
	}
	at, err := time.Parse(ybSnapshotRunIDLayout, runID)
	if err != nil {
		t.Fatalf("parse run key: %v", err)
	}
	if !at.Equal(generated) {
		t.Fatalf("run key parsed to %s, want %s", at, generated)
	}
}

// TestFDBRestorablePointFromStatus reads the restorable point out of the real
// `fdbbackup status` text of the pinned FoundationDB release, and refuses the
// output of a backup that has no restorable point yet.
func TestFDBRestorablePointFromStatus(t *testing.T) {
	ctx := context.Background()
	at, err := fdbRestorablePointFromStatus(ctx, fdbStatusRestorable)
	if err != nil {
		t.Fatalf("fdbRestorablePointFromStatus: %v", err)
	}
	want := time.Date(2026, 8, 30, 1, 7, 23, 0, time.UTC)
	if !at.Equal(want) {
		t.Fatalf("restorable point = %s, want %s", at.UTC(), want)
	}

	if _, err := fdbRestorablePointFromStatus(ctx, fdbStatusNotYetRestorable); err == nil {
		t.Fatal("a backup whose first snapshot has not finished has no restorable point")
	}
	if _, err := fdbRestorablePointFromStatus(ctx, "The backup on tag `default' is not restorable.\n"); err == nil {
		t.Fatal("a status that denies restorability must not be read as restorable")
	}
	if _, err := fdbRestorablePointFromStatus(ctx,
		"is restorable but continuing\n Last complete log version and timestamp - 5, not-a-timestamp\n"); err == nil {
		t.Fatal("an unparseable timestamp must be an error, not a silent success")
	}
}

// TestYBClusterHealthFromBody reads the master health payload the pinned
// YugabyteDB image serves: an all-clear cluster is healthy, a cluster with dead
// nodes or under-replicated tablets is not, and a payload that omits either
// list vouches for nothing.
func TestYBClusterHealthFromBody(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		body        string
		wantHealthy bool
		wantDetail  string
	}{
		{
			name:        "all clear",
			body:        ybHealthAllGood,
			wantHealthy: true,
			wantDetail:  "0 dead nodes, 0 under-replicated tablets",
		},
		{
			name:        "dead node",
			body:        `{"dead_nodes":["7ba1"],"most_recent_uptime":9,"under_replicated_tablets":[]}`,
			wantHealthy: false,
			wantDetail:  "1 dead nodes, 0 under-replicated tablets",
		},
		{
			name:        "under-replicated",
			body:        `{"dead_nodes":[],"most_recent_uptime":9,"under_replicated_tablets":["t1","t2"]}`,
			wantHealthy: false,
			wantDetail:  "0 dead nodes, 2 under-replicated tablets",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			healthy, detail, err := ybClusterHealthFromBody(ctx, []byte(test.body))
			if err != nil {
				t.Fatalf("ybClusterHealthFromBody: %v", err)
			}
			if healthy != test.wantHealthy {
				t.Fatalf("healthy = %v, want %v", healthy, test.wantHealthy)
			}
			if detail != test.wantDetail {
				t.Fatalf("detail = %q, want %q", detail, test.wantDetail)
			}
		})
	}

	for _, body := range []string{`{"most_recent_uptime":0}`, `{"dead_nodes":[]}`, "not json"} {
		if _, _, err := ybClusterHealthFromBody(ctx, []byte(body)); err == nil {
			t.Errorf("payload %q must not certify the cluster healthy", body)
		}
	}
}

// TestYBMasterHealthURLs proves the probe swaps the configured RPC port for the
// admin port and brackets IPv6 literals.
func TestYBMasterHealthURLs(t *testing.T) {
	tests := []struct {
		masters string
		want    []string
	}{
		{
			masters: "yugabyte:7100",
			want:    []string{"http://yugabyte:7000/api/v1/health-check"},
		},
		{
			masters: "yb1:7100,yb2:7100",
			want: []string{
				"http://yb1:7000/api/v1/health-check",
				"http://yb2:7000/api/v1/health-check",
			},
		},
		{
			masters: "[3d06:bad:b01::10]:7100",
			want:    []string{"http://[3d06:bad:b01::10]:7000/api/v1/health-check"},
		},
	}
	for _, test := range tests {
		got := ybMasterHealthURLs(test.masters)
		if strings.Join(got, " ") != strings.Join(test.want, " ") {
			t.Errorf("ybMasterHealthURLs(%q) = %v, want %v", test.masters, got, test.want)
		}
	}
}

// TestRunBackupStalenessCheckRequiresObjectStoreConfig proves the command
// refuses to report anything when it has no object store to read, rather than
// reporting every mechanism as unmeasurable.
func TestRunBackupStalenessCheckRequiresObjectStoreConfig(t *testing.T) {
	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), &config.Config{}, &out)
	if err == nil {
		t.Fatal("staleness-check must refuse to run without object-store credentials")
	}
	if !strings.Contains(err.Error(), "TACK_BACKUP_S3_ENDPOINT") {
		t.Fatalf("error must name the missing settings, got: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no report may be printed when the check cannot run: %q", out.String())
	}
}

// TestRunBackupStalenessCheckReportsUnreachableStoreAsStale runs the whole
// command against an object store and masters that refuse connections, the
// shape of a host where every backup mechanism is unmeasurable. Every metric
// must report unknown, the report must still reach the caller's writer (the
// alert mail is built from it), and the command must exit nonzero.
func TestRunBackupStalenessCheckReportsUnreachableStoreAsStale(t *testing.T) {
	nowFunc = func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = time.Now })

	cfg := &config.Config{
		BackupS3Endpoint:                     "http://127.0.0.1:1",
		BackupS3AccessKey:                    "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey:                    "test-secret", // gitleaks:allow test placeholder
		BackupS3Region:                       "us-east-1",
		BackupS3BucketMain:                   "tack-backups",
		BackupYBMasterAddresses:              "127.0.0.1:7100",
		BackupFDBContinuous:                  false,
		BackupStalenessExportMaxSeconds:      129600,
		BackupStalenessRehearsalMaxSeconds:   691200,
		BackupStalenessReplicationMaxSeconds: 1800,
		BackupStalenessFDBMaxSeconds:         7200,
	}
	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("unmeasurable backups must exit nonzero")
	}
	report := out.String()
	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if !strings.Contains(report, name) {
			t.Errorf("report is missing the %s line:\n%s", name, report)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error is missing the stale mechanism %s: %v", name, err)
		}
	}
	if strings.Contains(report, backupStalenessFDBName) {
		t.Errorf("the FoundationDB metric must be skipped when continuous backup is off:\n%s", report)
	}
	if strings.Count(report, "STALE") != 3 || strings.Contains(report, "FRESH") {
		t.Errorf("every unmeasurable mechanism must read STALE:\n%s", report)
	}
	if strings.Count(report, "age=unknown") != 3 {
		t.Errorf("every unmeasurable mechanism must report an unknown age:\n%s", report)
	}
}
