package resourcesummary_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const finalWindowLine = 4

func replaceOnce(old string, replacement string) func(string) string {
	return func(input string) string {
		return strings.Replace(input, old, replacement, 1)
	}
}

func replaceEvery(old string, replacement string) func(string) string {
	return func(input string) string {
		return strings.ReplaceAll(input, old, replacement)
	}
}

// zeroFinalCounters sets the counters that match pattern to zero in the last sampler line
// inside the test window.
func zeroFinalCounters(pattern string) func(string) string {
	counters := regexp.MustCompile(pattern)
	return func(input string) string {
		lines := strings.Split(input, "\n")
		lines[finalWindowLine] = counters.ReplaceAllStringFunc(
			lines[finalWindowLine],
			func(field string) string {
				key, _, _ := strings.Cut(field, "=")
				return key + "=0"
			},
		)
		return strings.Join(lines, "\n")
	}
}

// writeChangedFixture applies change to the fixture and writes the result to a temporary file.
func writeChangedFixture(
	t *testing.T,
	fixturePath string,
	change func(string) string,
) string {
	t.Helper()
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	changed := change(string(fixture))
	if changed == string(fixture) {
		t.Fatalf("the change did not alter %s", fixturePath)
	}
	path := filepath.Join(t.TempDir(), filepath.Base(fixturePath))
	err = os.WriteFile(path, []byte(changed), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestSummarizeHostRejectsInvalidMeasurements changes one measurement in the sampler fixture
// for each case and checks the exact error from SummarizeHost.
func TestSummarizeHostRejectsInvalidMeasurements(t *testing.T) {
	cases := []struct {
		name      string
		change    func(string) string
		wantError string
	}{
		{
			name:   "pressure NaN",
			change: replaceOnce("some avg10=1.50", "some avg10=NaN"),
			wantError: `sample 2026-01-01T00:00:00Z: host memory pressure: parse avg10 in ` +
				`"some avg10=NaN avg60=0.00 avg300=0.00 total=10": ` +
				`value must be finite and not negative`,
		},
		{
			name:   "load infinity",
			change: replaceOnce("4.00 3.00 2.00 3/100 200", "+Inf 3.00 2.00 3/100 200"),
			wantError: `sample 2026-01-01T00:00:00Z: parse loadavg ` +
				`"+Inf 3.00 2.00 3/100 200": value must be finite and not negative`,
		},
		{
			name:   "pressure negative",
			change: replaceEvery("full avg10=0.00", "full avg10=-1.00"),
			wantError: `sample 2026-01-01T00:00:00Z: host CPU pressure: parse avg10 in ` +
				`"full avg10=-1.00 avg60=0.00 avg300=0.00 total=0": ` +
				`value must be finite and not negative`,
		},
		{
			name: "both io counters missing",
			change: func(input string) string {
				withoutRead := strings.ReplaceAll(input, "rbytes=", "omitted_rbytes=")
				return strings.ReplaceAll(withoutRead, "wbytes=", "omitted_wbytes=")
			},
			wantError: `sample 2026-01-01T00:00:00Z: find rbytes in io.stat line ` +
				`"8:0 omitted_rbytes=1048576 omitted_wbytes=0 rios=1 wios=0 dbytes=0 dios=0": ` +
				`io.stat counter is missing`,
		},
		{
			name:   "write counter missing",
			change: replaceEvery("wbytes=", "omitted_wbytes="),
			wantError: `sample 2026-01-01T00:00:00Z: find wbytes in io.stat line ` +
				`"8:0 rbytes=1048576 omitted_wbytes=0 rios=1 wios=0 dbytes=0 dios=0": ` +
				`io.stat counter is missing`,
		},
		{
			name:   "write counter missing from one device line",
			change: replaceOnce("wbytes=2097152", "omitted_wbytes=2097152"),
			wantError: `sample 2026-01-01T00:00:00Z: find wbytes in io.stat line ` +
				`"8:16 rbytes=0 omitted_wbytes=2097152 rios=0 wios=1 dbytes=0 dios=0": ` +
				`io.stat counter is missing`,
		},
		{
			name:      "MemAvailable missing",
			change:    replaceEvery(`"MemAvailable":`, `"OtherMemory":`),
			wantError: `sample 2026-01-01T00:00:00Z: meminfo_kib.MemAvailable is missing`,
		},
		{
			name:      "CPU counter reset",
			change:    replaceOnce(`"usage_usec": 31000000`, `"usage_usec": 0`),
			wantError: `sample 2026-01-01T00:01:00Z: guest usage_usec decreased from 16000000 to 0`,
		},
		{
			name:      "IO counters reset",
			change:    zeroFinalCounters(`[rw]bytes=[0-9]+`),
			wantError: `sample 2026-01-01T00:01:00Z: guest rbytes decreased from 5242880 to 0`,
		},
		{
			name:      "write counter reset",
			change:    zeroFinalCounters(`wbytes=[0-9]+`),
			wantError: `sample 2026-01-01T00:01:00Z: guest wbytes decreased from 3145728 to 0`,
		},
	}
	windowStart := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	window := resourcesummary.Window{Start: windowStart, End: windowStart.Add(time.Minute)}
	for _, scenario := range cases {
		t.Run(
			scenario.name,
			func(t *testing.T) {
				path := writeChangedFixture(t, samplerFixture, scenario.change)

				got, err := resourcesummary.SummarizeHost(t.Context(), path, window, searchGuestID)

				if err == nil {
					t.Fatalf("summary = %+v, want error %q", got, scenario.wantError)
				}
				if err.Error() != scenario.wantError {
					t.Errorf("error = %q, want %q", err.Error(), scenario.wantError)
				}
			},
		)
	}
}
