package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func requireVerdictReadRow(t *testing.T, row audit.Row, want readRowTarget, result json.RawMessage, itemURIs []string, messagePart string) {
	t.Helper()
	requireReadRowShape(t, row, audit.OutcomeFailed, want)
	requireReadEvidence(t, row, result, itemURIs)
	if row.Error == nil || row.Error.Code != "check_failed" || !strings.Contains(row.Error.Message, messagePart) {
		t.Fatalf("row %s error %+v, want check_failed containing %q", row.EventID, row.Error, messagePart)
	}
}

func requireReadEvidence(t *testing.T, row audit.Row, result json.RawMessage, itemURIs []string) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, result); err != nil {
		t.Fatalf("compact the command result: %v", err)
	}
	fingerprint := sha256.Sum256(compact.Bytes())
	manifestDigest, manifestParts := expectedManifestEvidence(t, itemURIs)
	read := row.Context.Read
	if read == nil || read.PayloadSHA256 != hex.EncodeToString(fingerprint[:]) || read.PayloadFormat != readLifecycleJSONFormat ||
		read.Count != uint64(len(itemURIs)) || read.ManifestSHA256 != manifestDigest || read.ManifestParts != manifestParts {
		t.Fatalf("row %s read evidence = %+v, want fingerprint %x, %d items, manifest %q with %d parts",
			row.EventID, read, fingerprint, len(itemURIs), manifestDigest, manifestParts)
	}
}
