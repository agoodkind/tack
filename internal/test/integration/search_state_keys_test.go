package integration_test

import (
	"bytes"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

const stateFamilyPrefix = "search_"

// stateKey stores a search key's family and decoded tuple elements for state
// assertions.
type stateKey struct {
	family   string
	elements tuple.Tuple
}

// readStateKeys reads every key under the test prefix with a family starting
// with search_ and decodes its tuple elements. It fails the test if a database
// operation fails or a key lacks a valid string family.
func readStateKeys(t *testing.T, fixture parentEdgeFixture) []stateKey {
	t.Helper()
	database, err := fdbadapter.Open(fixture.cfg.FDBClusterFile, cohortTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to read search keys: %v", err)
	}
	prefix := bytes.Clone(fdbadapter.TestPrefixRange())
	packedFamily := tuple.Tuple{stateFamilyPrefix}.Pack()
	familyStart := append(bytes.Clone(prefix), packedFamily[:len(packedFamily)-1]...)
	keyRange, err := fdb.PrefixRange(familyStart)
	if err != nil {
		t.Fatalf("create search key range: %v", err)
	}
	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create search key transaction: %v", err)
	}
	defer transaction.Cancel()
	entries, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read search keys: %v", err)
	}
	keys := make([]stateKey, 0, len(entries))
	for _, entry := range entries {
		elements, decodeErr := tuple.Unpack(entry.Key[len(prefix):])
		if decodeErr != nil || len(elements) == 0 {
			t.Fatalf("decode search key %x: %v", entry.Key, decodeErr)
		}
		family, isText := elements[0].(string)
		if !isText {
			t.Fatalf("search key %x has family %v, want a string", entry.Key, elements[0])
		}
		keys = append(keys, stateKey{family: family, elements: elements})
	}
	return keys
}

func (key stateKey) text(t *testing.T, position int) string {
	t.Helper()
	if position >= len(key.elements) {
		t.Fatalf("%s key %v has no element %d", key.family, key.elements, position)
	}
	value, isText := key.elements[position].(string)
	if !isText {
		t.Fatalf("%s key %v element %d is %T, want a string", key.family, key.elements, position, key.elements[position])
	}
	return value
}

func (key stateKey) integer(t *testing.T, position int) int64 {
	t.Helper()
	if position >= len(key.elements) {
		t.Fatalf("%s key %v has no element %d", key.family, key.elements, position)
	}
	value, isInteger := key.elements[position].(int64)
	if !isInteger {
		t.Fatalf("%s key %v element %d is %T, want an integer", key.family, key.elements, position, key.elements[position])
	}
	return value
}

func (key stateKey) references(identifiers map[string]bool) bool {
	for _, element := range key.elements {
		value, isText := element.(string)
		if isText && identifiers[value] {
			return true
		}
	}
	return false
}

// countStateFamilies counts keys by family, excluding any key with a string
// element that matches an identifier in excluded.
func countStateFamilies(keys []stateKey, excluded map[string]bool) map[string]int {
	counts := make(map[string]int)
	for _, key := range keys {
		if key.references(excluded) {
			continue
		}
		counts[key.family]++
	}
	return counts
}

func stateKeysOf(keys []stateKey, family string) []stateKey {
	matching := make([]stateKey, 0, len(keys))
	for _, key := range keys {
		if key.family == family {
			matching = append(matching, key)
		}
	}
	return matching
}
