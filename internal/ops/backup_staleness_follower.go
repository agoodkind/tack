// Follower masters can answer the health check with HTTP 200 and a page
// about the leader URL instead of the health payload. A probe can receive
// these pages while checking masters before a leader answers. It recognizes
// the follower page and continues quietly instead of reporting an unreadable
// health payload.

package ops

import "bytes"

// ybMasterFollowerPagePrefix identifies the start of a follower master's
// health-check page. The original capture records production on 2026-09-13
// with yugabytedb 2024.2.8.0:
//
//	Error retrieving leader master URL: <a href="http://yb1:7000/api/v1/health-check?raw">...
const ybMasterFollowerPagePrefix = "Error retrieving leader master URL"

// ybMasterAnswersAsFollower reports whether body is a follower's page rather
// than a health payload.
func ybMasterAnswersAsFollower(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(body), []byte(ybMasterFollowerPagePrefix))
}
