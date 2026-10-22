package integration_test

import (
	"crypto/sha256"
	"fmt"
	hashfnv "hash/fnv"
	"testing"

	"github.com/google/uuid"
)

const (
	sessionBucketCount     = 32
	spreadSessions         = 64
	spreadSessionBuckets   = 16
	spreadSessionsPerShare = 16
	spreadNodes            = 256
	spreadWorkBuckets      = 128
	spreadWorkPerBucket    = 16
	spreadText             = "spread bucket text"
	workFamily             = "search_work"
	ageFamily              = "search_age"
	claimFamily            = "search_claim"
	generationFamily       = "search_generation"
	liveClass              = "live"
	generationNodePosition = 2
)

type workKeyLayout struct {
	class, bucket, org, node int
}

// workKeyLayouts maps each supported work key family to the tuple positions
// of its work class, bucket, organization ID, and node ID.
var workKeyLayouts = map[string]workKeyLayout{
	workFamily:  {class: 1, bucket: 2, org: 3, node: 4},
	ageFamily:   {class: 1, bucket: 2, org: 4, node: 5},
	claimFamily: {class: 1, bucket: 2, org: 3, node: 4},
}

// TestSearchBucketsSpreadSessionsAndWork requires search keys to use buckets
// that match their session or node identities. Sessions and live work must
// meet the minimum bucket counts and maximum occupancy limits, and each new
// node must have one generation counter.
// Claims must stop at the oldest claimScanLimit items in a crowded bucket
// while those items have active leases. Worker slices must leave newer items
// pending until those leases expire, then finish all live work in the bucket.
func TestSearchBucketsSpreadSessionsAndWork(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	fixture.drain(t)
	requireBoundedClaimScan(t, fixture)
	requireWorkSpread(t, fixture)
	requireSessionSpread(t, fixture)
}

// sessionBucket returns the 32-bit FNV-1a hash of sessionID modulo
// sessionBucketCount.
func sessionBucket(sessionID uuid.UUID) int64 {
	hasher := hashfnv.New32a()
	_, _ = hasher.Write(sessionID[:])
	return int64(hasher.Sum32() % sessionBucketCount)
}

// workBucket returns the first byte of the SHA-256 digest of the bytes of
// orgID followed by the bytes of nodeID.
func workBucket(orgID, nodeID uuid.UUID) int64 {
	identity := make([]byte, 0, len(orgID)+len(nodeID))
	identity = append(append(identity, orgID[:]...), nodeID[:]...)
	digest := sha256.Sum256(identity)
	return int64(digest[0])
}

func requireWorkKeyBuckets(t *testing.T, keys []stateKey) {
	t.Helper()
	for _, key := range keys {
		layout, isWork := workKeyLayouts[key.family]
		if !isWork {
			continue
		}
		orgID := uuid.MustParse(key.text(t, layout.org))
		nodeID := uuid.MustParse(key.text(t, layout.node))
		if bucket := key.integer(t, layout.bucket); bucket != workBucket(orgID, nodeID) {
			t.Fatalf("%s key of node %s uses bucket %d, want %d", key.family, nodeID, bucket, workBucket(orgID, nodeID))
		}
	}
}

// requireWorkSpread creates leaf nodes and requires their live work to meet
// the bucket count and occupancy limits. It also checks each stored work,
// age, and claim key's hash bucket and requires one generation counter per
// new node.
func requireWorkSpread(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	created := make(map[string]bool, spreadNodes)
	for range spreadNodes {
		created[fixture.createLeaf(t, fixture.firstEntry, spreadText).String()] = true
	}
	keys := readStateKeys(t, fixture)
	requireWorkKeyBuckets(t, keys)
	perBucket := make(map[int64]int)
	layout := workKeyLayouts[workFamily]
	for _, key := range stateKeysOf(keys, workFamily) {
		if key.text(t, layout.class) == liveClass && created[key.text(t, layout.node)] {
			perBucket[key.integer(t, layout.bucket)]++
		}
	}
	requireSpread(t, "live work items", perBucket, spreadNodes, spreadWorkBuckets, spreadWorkPerBucket)
	counters := 0
	for _, key := range stateKeysOf(keys, generationFamily) {
		if created[key.text(t, generationNodePosition)] {
			counters++
		}
	}
	if counters != spreadNodes {
		t.Fatalf("%d nodes wrote %d search generation counters, want one counter for each node", spreadNodes, counters)
	}
}

// requireSessionSpread opens distinct searches and requires every stored
// session key to use its session ID's hash bucket. It counts session headers
// to check the bucket count and occupancy limits.
func requireSessionSpread(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	for number := range spreadSessions {
		searchPageOf(t, fixture, fmt.Sprintf("spread session %d", number), "")
	}
	perBucket := make(map[int64]int)
	for _, key := range readStateKeys(t, fixture) {
		layout, isSession := sessionKeyLayouts[key.family]
		if !isSession {
			continue
		}
		sessionID := uuid.MustParse(key.text(t, layout.session))
		bucket := key.integer(t, layout.bucket)
		if bucket != sessionBucket(sessionID) {
			t.Fatalf("%s key of session %s uses bucket %d, want %d", key.family, sessionID, bucket, sessionBucket(sessionID))
		}
		if key.family == sessionHeaderFamily {
			perBucket[bucket]++
		}
	}
	requireSpread(t, "sessions", perBucket, spreadSessions, spreadSessionBuckets, spreadSessionsPerShare)
}

// requireSpread fails the test if the counts do not sum to total, use fewer
// than minimumBuckets buckets, or exceed maximumPerBucket in any bucket.
func requireSpread(t *testing.T, subject string, perBucket map[int64]int, total, minimumBuckets, maximumPerBucket int) {
	t.Helper()
	stored, largest := 0, 0
	for _, count := range perBucket {
		stored, largest = stored+count, max(largest, count)
	}
	if stored != total || len(perBucket) < minimumBuckets || largest > maximumPerBucket {
		t.Fatalf("%d %s use %d buckets with at most %d in one bucket, want %d in at least %d buckets with at most %d in one bucket",
			stored, subject, len(perBucket), largest, total, minimumBuckets, maximumPerBucket)
	}
}
