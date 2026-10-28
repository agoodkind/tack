package integration

import (
	"bytes"
	"encoding/json"
	"testing"
)

// claimOperation is the message value for search work claim log records.
const claimOperation = "store.search_work.claim"

// claimOperationRecord stores the message, status, and error fields decoded
// from one JSON log record.
type claimOperationRecord struct {
	Message string `json:"msg"`
	Status  string `json:"status"`
	Err     string `json:"err"`
}

// requireNoFailedClaim decodes each JSON record in logged and fails the
// test if a record has message store.search_work.claim and status failed.
func requireNoFailedClaim(t *testing.T, logged *bytes.Buffer) {
	t.Helper()
	decoder := json.NewDecoder(logged)
	for decoder.More() {
		var record claimOperationRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode a claim log record: %v", err)
		}
		if record.Message == claimOperation && record.Status == "failed" {
			t.Fatalf("an empty claim logged a failed %s record with error %q", claimOperation, record.Err)
		}
	}
}
