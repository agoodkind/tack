package audit

import "testing"

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
