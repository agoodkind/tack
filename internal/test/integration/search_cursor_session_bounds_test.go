package integration_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sessionChildPartPosition is the tuple position of the child part name.
	sessionChildPartPosition = 3
	// sessionTokenPart, sessionVisitedPart, and sessionReplayPart are the
	// child part names of token chunks, visited markers, and replay records.
	sessionTokenPart   = "token"
	sessionVisitedPart = "visited"
	sessionReplayPart  = "replay"
	// sessionHeaderBound and sessionReplayBound are the stored byte limits of
	// one session header and one replay record.
	sessionHeaderBound = 64 << 10
	sessionReplayBound = 64 << 10
	// sessionTokenChunkBound is the stored byte limit of one token chunk.
	sessionTokenChunkBound = 16 << 10
)

// sessionRecords reads every key and value of the search session key
// families under the test prefix that has sessionID as a tuple element. It
// returns the values by packed key.
func sessionRecords(t *testing.T, sessionID uuid.UUID) map[string][]byte {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), cohortTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to read session records: %v", err)
	}
	prefix := bytes.Clone(fdbadapter.TestPrefixRange())
	packed := tuple.Tuple{sessionHeaderFamily}.Pack()
	keyRange, err := fdb.PrefixRange(append(bytes.Clone(prefix), packed[:len(packed)-1]...))
	if err != nil {
		t.Fatalf("create session key range: %v", err)
	}
	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create session key transaction: %v", err)
	}
	defer transaction.Cancel()
	entries, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read session keys: %v", err)
	}
	records := make(map[string][]byte)
	for _, entry := range entries {
		elements, decodeErr := tuple.Unpack(entry.Key[len(prefix):])
		if decodeErr != nil {
			t.Fatalf("decode session key %x: %v", entry.Key, decodeErr)
		}
		for _, element := range elements {
			if element == sessionID.String() {
				records[string(entry.Key)] = entry.Value
				break
			}
		}
	}
	return records
}

// requireSessionUnchanged requires a call to leave every stored record of
// the session byte for byte unchanged.
func requireSessionUnchanged(t *testing.T, call string, before, after map[string][]byte) {
	t.Helper()
	if !maps.EqualFunc(before, after, bytes.Equal) {
		t.Fatalf("%s changed the stored session records: %d records before, %d after", call, len(before), len(after))
	}
}

// storedSessionHeader decodes the commit version and token chunk count of
// one stored session header.
type storedSessionHeader struct {
	Version     uint64 `json:"version"`
	TokenChunks int    `json:"token_chunks"`
}

// requireBoundedSession requires the stored records of one open session to
// contain one header at version commits, the header's token chunks, one
// visited marker per distinct node in results, one replay record, one expiry
// key, and at most one presence key and one version key. It also requires
// each value to fit the byte limit of its record kind.
func requireBoundedSession(t *testing.T, records map[string][]byte, results []uuid.UUID, commits uint64) {
	t.Helper()
	prefix := fdbadapter.TestPrefixRange()
	counts := make(map[string]int)
	var header storedSessionHeader
	for key, value := range records {
		elements, err := tuple.Unpack([]byte(key)[len(prefix):])
		if err != nil {
			t.Fatalf("decode session key %x: %v", key, err)
		}
		kind, _ := elements[0].(string)
		if kind == sessionChildFamily {
			kind, _ = elements[sessionChildPartPosition].(string)
		}
		counts[kind]++
		requireSessionValueBound(t, kind, value)
		if kind == sessionHeaderFamily {
			if err := json.Unmarshal(value, &header); err != nil {
				t.Fatalf("decode session header: %v", err)
			}
		}
	}
	distinct := make(map[uuid.UUID]bool, len(results))
	for _, id := range results {
		distinct[id] = true
	}
	if header.Version != commits || counts[sessionHeaderFamily] != 1 || counts[sessionTokenPart] != header.TokenChunks ||
		counts[sessionVisitedPart] != len(distinct) || counts[sessionReplayPart] != 1 || counts[sessionExpiryFamily] != 1 ||
		counts[sessionPresentFamily] > 1 || counts[sessionVersionFamily] > 1 {
		t.Fatalf("session stores version %d and records %v, want version %d, one header, %d token chunks, %d visited markers, one replay record, one expiry key, and at most one presence and version key",
			header.Version, counts, commits, header.TokenChunks, len(distinct))
	}
}

// requireSessionValueBound fails the test when one stored session value
// exceeds the limit of its record kind. A kind without a limit is a marker
// key with an empty value.
func requireSessionValueBound(t *testing.T, kind string, value []byte) {
	t.Helper()
	limits := map[string]int{
		sessionHeaderFamily: sessionHeaderBound, sessionReplayPart: sessionReplayBound,
		sessionTokenPart: sessionTokenChunkBound, sessionVisitedPart: 1,
	}
	if limit := limits[kind]; len(value) > limit {
		t.Fatalf("stored session %s record contains %d bytes, above the %d-byte limit", kind, len(value), limit)
	}
}
