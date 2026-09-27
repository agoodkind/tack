package foundationdb

import (
	hashfnv "hash/fnv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// Durable query session key families. Session keys use a stable hash
// bucket of the session ID, and concurrent sessions use separate ranges.
// (search_session, bucket, sessionID) -> bounded session header JSON
// (search_session_child, bucket, sessionID, "token", chunk) -> token bytes
// (search_session_child, bucket, sessionID, "visited", nodeID) -> marker
// (search_session_child, bucket, sessionID, "replay", version) -> replay JSON
// (search_session_expiry, minute, bucket, sessionID) -> nil
// (search_session_present, index, bucket, sessionID) -> nil
const (
	keySearchSession        = "search_session"
	keySearchSessionChild   = "search_session_child"
	keySearchSessionExpiry  = "search_session_expiry"
	keySearchSessionPresent = "search_session_present"
)

const (
	searchSessionBuckets = 32
	sessionTokenPart     = "token"
	sessionVisitedPart   = "visited"
	sessionReplayPart    = "replay"
)

func searchSessionBucket(sessionID uuid.UUID) int {
	hasher := hashfnv.New32a()
	_, _ = hasher.Write(sessionID[:])
	return int(hasher.Sum32() % searchSessionBuckets)
}

func searchSessionKey(sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchSession, searchSessionBucket(sessionID), sessionID.String()}.Pack())
}

func searchSessionChildPrefix(sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionChild, searchSessionBucket(sessionID), sessionID.String()}.Pack())
}

func searchSessionPartPrefix(sessionID uuid.UUID, part string) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionChild, searchSessionBucket(sessionID), sessionID.String(), part}.Pack())
}

func searchSessionTokenKey(sessionID uuid.UUID, chunk int) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionChild, searchSessionBucket(sessionID), sessionID.String(), sessionTokenPart, chunk}.Pack())
}

func searchSessionVisitedKey(sessionID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionChild, searchSessionBucket(sessionID), sessionID.String(), sessionVisitedPart, nodeID.String()}.Pack())
}

func searchSessionReplayKey(sessionID uuid.UUID, version uint64) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionChild, searchSessionBucket(sessionID), sessionID.String(), sessionReplayPart, version}.Pack())
}

// searchSessionMinute is the expiry bucket of one deadline.
func searchSessionMinute(deadline time.Time) int64 {
	return deadline.UTC().Unix() / int64(time.Minute/time.Second)
}

func searchSessionExpiryKey(sessionID uuid.UUID, deadline time.Time) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionExpiry, searchSessionMinute(deadline), searchSessionBucket(sessionID), sessionID.String()}.Pack())
}

// searchSessionExpiryRange covers every expiry key with a minute before end.
func searchSessionExpiryRange(end time.Time) (begin, until []byte) {
	begin = withPrefix(tuple.Tuple{keySearchSessionExpiry}.Pack())
	until = withPrefix(tuple.Tuple{keySearchSessionExpiry, searchSessionMinute(end)}.Pack())
	return begin, until
}

func searchSessionPresenceKey(index string, sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionPresent, index, searchSessionBucket(sessionID), sessionID.String()}.Pack())
}
