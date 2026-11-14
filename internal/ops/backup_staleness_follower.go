// A follower master answers health checks with HTTP 200 and the leader's URL
// instead of a health payload. The probe recognizes this response and checks
// the next master without reporting an unreadable payload.

package ops

import "bytes"

// ybMasterFollowerPagePrefix opens the page a follower master serves for the
// health check. Captured from production on 2026-09-13 (yugabytedb
// 2024.2.8.0):
//
//	Error retrieving leader master URL: <a href="http://yb1:7000/api/v1/health-check?raw">...
const ybMasterFollowerPagePrefix = "Error retrieving leader master URL"

// ybMasterAnswersAsFollower reports whether body is a follower's page rather
// than a health payload.
func ybMasterAnswersAsFollower(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(body), []byte(ybMasterFollowerPagePrefix))
}
