package foundationdb

import (
	"fmt"
	hashfnv "hash/fnv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// These constants define the durable query session key families. Session
// keys include a stable hash bucket of the session ID. Concurrent sessions
// write separate key ranges.
//
//	(search_session, bucket, sessionID) -> bounded session header JSON
//	(search_session_child, bucket, sessionID, "token", chunk) -> token bytes
//	(search_session_child, bucket, sessionID, "visited", nodeID) -> marker
//	(search_session_child, bucket, sessionID, "replay", version) -> replay JSON
//	(search_session_expiry, minute, bucket, sessionID) -> nil
//	(search_session_present, index, bucket, sessionID) -> nil
//	(search_session_version, authorityID, accessVersion, absoluteMinute, bucket, sessionID) -> nil
//	(search_retire_attempt, index, sessionID, step, eventID) -> JSON containing the attempt lease deadline and encoded failure event
//
// The search_session_version family stores one entry for each session that
// can still read OpenSearch under that access version.
const (
	keySearchSession        = "search_session"
	keySearchSessionChild   = "search_session_child"
	keySearchSessionExpiry  = "search_session_expiry"
	keySearchSessionPresent = "search_session_present"
	keySearchSessionVersion = "search_session_version"
	keySearchRetireAttempt  = "search_retire_attempt"
)

const (
	retireAttemptKeyElements = 5
	searchSessionBuckets     = 32
	sessionTokenPart         = "token"
	sessionVisitedPart       = "visited"
	sessionReplayPart        = "replay"
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

// searchSessionMinute returns the expiry minute bucket of one deadline.
func searchSessionMinute(deadline time.Time) int64 {
	return deadline.UTC().Unix() / int64(time.Minute/time.Second)
}

func searchSessionExpiryKey(sessionID uuid.UUID, deadline time.Time) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionExpiry, searchSessionMinute(deadline), searchSessionBucket(sessionID), sessionID.String()}.Pack())
}

// searchSessionExpiryRange returns the key range of every expiry key with a
// minute bucket earlier than the minute that contains end.
func searchSessionExpiryRange(end time.Time) (begin, until []byte) {
	begin = withPrefix(tuple.Tuple{keySearchSessionExpiry}.Pack())
	until = withPrefix(tuple.Tuple{keySearchSessionExpiry, searchSessionMinute(end)}.Pack())
	return begin, until
}

func searchSessionPresenceKey(index string, sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionPresent, index, searchSessionBucket(sessionID), sessionID.String()}.Pack())
}

func searchRetireAttemptKey(index string, sessionID uuid.UUID, step searchdomain.CleanupStep, eventID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchRetireAttempt, index, sessionID.String(), string(step), eventID.String()}.Pack())
}

func searchRetireAttemptPrefix(index string) []byte {
	return withPrefix(tuple.Tuple{keySearchRetireAttempt, index}.Pack())
}

func parseRetireAttemptKey(key fdb.Key) (uuid.UUID, searchdomain.CleanupStep, uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, "", uuid.Nil, sessionStepError{operation: "unpack retirement attempt key", err: err}
	}
	if len(values) != retireAttemptKeyElements {
		return uuid.Nil, "", uuid.Nil, fmt.Errorf("retirement attempt key has %d elements, want %d", len(values), retireAttemptKeyElements)
	}
	sessionText, sessionOK := values[2].(string)
	stepText, stepOK := values[3].(string)
	eventText, eventOK := values[4].(string)
	if !sessionOK || !stepOK || !eventOK {
		return uuid.Nil, "", uuid.Nil, fmt.Errorf("retirement attempt key %x does not end with text values", key)
	}
	sessionID, err := uuid.Parse(sessionText)
	if err != nil {
		return uuid.Nil, "", uuid.Nil, sessionStepError{operation: "parse retirement attempt session ID", err: err}
	}
	eventID, err := uuid.Parse(eventText)
	if err != nil {
		return uuid.Nil, "", uuid.Nil, sessionStepError{operation: "parse retirement attempt event ID", err: err}
	}
	return sessionID, searchdomain.CleanupStep(stepText), eventID, nil
}

// searchSessionVersionKey orders one session's version presence entry by
// the minute of its absolute deadline.
func searchSessionVersionKey(authorityID uuid.UUID, version string, absolute time.Time, sessionID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{
		keySearchSessionVersion, authorityID.String(), version, searchSessionMinute(absolute),
		searchSessionBucket(sessionID), sessionID.String(),
	}.Pack())
}

// searchSessionVersionPrefix returns the key prefix of every version presence
// entry of one authority and access version.
func searchSessionVersionPrefix(authorityID uuid.UUID, version string) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionVersion, authorityID.String(), version}.Pack())
}

// searchSessionVersionStart returns the lower-bound key of the version
// presence entries with an absolute deadline in the current minute or later.
func searchSessionVersionStart(authorityID uuid.UUID, version string, now time.Time) []byte {
	return withPrefix(tuple.Tuple{keySearchSessionVersion, authorityID.String(), version, searchSessionMinute(now)}.Pack())
}
