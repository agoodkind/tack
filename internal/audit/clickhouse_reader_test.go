package audit

import "testing"

// TestBothReadersAgreeOnOutcome pins the contract this path exists for: the
// same stored event reports the same outcome whether it is read from the
// ledger or from the analytics tier. A row written before the ledger stored
// outcomes reads as unrecorded on both, never as a success nobody observed.
func TestBothReadersAgreeOnOutcome(t *testing.T) {
	cases := []struct {
		name       string
		clickHouse string
		yugabyte   *string
		want       Outcome
	}{
		{name: "missing outcome", clickHouse: "", yugabyte: nil, want: OutcomeUnrecorded},
		{name: "success", clickHouse: "ok", yugabyte: stringPointer("ok"), want: OutcomeOK},
		{name: "failure", clickHouse: "error", yugabyte: stringPointer("error"), want: OutcomeError},
		{name: "pending intent", clickHouse: "pending", yugabyte: stringPointer("pending"), want: OutcomePending},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fromClickHouse := clickHouseOutcomeFromColumn(testCase.clickHouse)
			fromYugabyte := outcomeFromColumn(testCase.yugabyte)
			if fromClickHouse != testCase.want {
				t.Fatalf("analytics outcome = %q, want %q", fromClickHouse, testCase.want)
			}
			if fromYugabyte != testCase.want {
				t.Fatalf("ledger outcome = %q, want %q", fromYugabyte, testCase.want)
			}
			if fromClickHouse != fromYugabyte {
				t.Fatalf("readers disagree: analytics %q, ledger %q", fromClickHouse, fromYugabyte)
			}
		})
	}
}

// TestJSONNullIfEmptyMatchesTheConsumer pins that a reconciled row carries an
// absent optional payload the same way a freshly projected row does. Without
// this the two paths write different bytes for the same missing value, and a
// reader decoding one of them fails.
func TestJSONNullIfEmptyMatchesTheConsumer(t *testing.T) {
	if got := string(jsonNullIfEmpty(nil)); got != "null" {
		t.Fatalf("absent payload = %q, want null", got)
	}
	if got := string(jsonNullIfEmpty([]byte{})); got != "null" {
		t.Fatalf("empty payload = %q, want null", got)
	}
	present := []byte(`{"op_id":"019dd222-440e-729a-a442-281aaf73ca30"}`)
	if got := string(jsonNullIfEmpty(present)); got != string(present) {
		t.Fatalf("present payload = %q, want it unchanged", got)
	}
}

func stringPointer(value string) *string { return &value }
