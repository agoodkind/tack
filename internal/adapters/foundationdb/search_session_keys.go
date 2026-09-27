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
// (search_session_version, authorityID, accessVersion, absoluteMinute,
//
//	bucket, sessionID) -> nil, one entry per session that can still read
//	OpenSearch under that access version
const (
	keySearchSession        = "search_session"
	keySearchSessionChild   = "search_session_child"
	keySearchSessionExpiry  = "search_session_expiry"
	keySearchSessionPresent = "search_session_present"
	keySearchSessionVersion = "search_session_version"
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

// searchSessionVersionKey orders one session's version presence entry by
// the minute of its absolute deadline.
func searchSessionVersionKey(authorityID uuid.UUID, version string, absolute time.Time, sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{
		keySearchSessionVersion, authorityID.String(), version, searchSessionMinute(absolute),
		searchSessionBucket(sessionID), sessionID.String(),
	}.Pack())
}

// searchSessionVersionPrefix covers every version presence entry of one
// authority and access version.
func searchSessionVersionPrefix(authorityID uuid.UUID, version string) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionVersion, authorityID.String(), version}.Pack())
}

// searchSessionVersionStart is the first version presence key with an
// absolute deadline in the current minute or later.
func searchSessionVersionStart(authorityID uuid.UUID, version string, now time.Time) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionVersion, authorityID.String(), version, searchSessionMinute(now)}.Pack())
}
