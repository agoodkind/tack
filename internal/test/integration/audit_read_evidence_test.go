package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func requireVerdictReadRow(t *testing.T, row audit.Row, want readRowTarget, printed printedOutput, itemURIs []string, messagePart string) {
	t.Helper()
	requireReadRowShape(t, row, audit.OutcomeFailed, want)
	requireReadEvidence(t, row, printed, itemURIs)
	if row.Error == nil || row.Error.Code != "check_failed" || !strings.Contains(row.Error.Message, messagePart) {
		t.Fatalf("row %s error %+v, want check_failed containing %q", row.EventID, row.Error, messagePart)
	}
}

type printedOutput []byte

func requireReadEvidence(t *testing.T, row audit.Row, printed printedOutput, itemURIs []string) {
	t.Helper()
	fingerprint := sha256.Sum256(printed)
	manifestDigest, manifestParts := expectedManifestEvidence(t, itemURIs)
	read := row.Context.Read
	if read == nil || read.PayloadSHA256 != hex.EncodeToString(fingerprint[:]) || read.PayloadFormat != readLifecycleJSONFormat ||
		read.Count != uint64(len(itemURIs)) || read.ManifestSHA256 != manifestDigest || read.ManifestParts != manifestParts {
		t.Fatalf("row %s read evidence = %+v, want fingerprint %x, %d items, manifest %q with %d parts",
			row.EventID, read, fingerprint, len(itemURIs), manifestDigest, manifestParts)
	}
}
