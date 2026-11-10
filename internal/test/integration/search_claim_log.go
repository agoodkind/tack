package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
// It also fails the test on every decode error other than [io.EOF], which
// marks the end of the buffer.
func requireNoFailedClaim(t *testing.T, logged *bytes.Buffer) {
	t.Helper()
	decoder := json.NewDecoder(logged)
	for {
		var record claimOperationRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("decode a claim log record: %v", err)
		}
		if record.Message == claimOperation && record.Status == "failed" {
			t.Fatalf("an empty claim logged a failed %s record with error %q", claimOperation, record.Err)
		}
	}
}
