// backup_staleness_follower.go recognizes a follower master's answer to the
// health check. A follower answers 200 with a page that names the leader's URL
// instead of the health payload, so a probe that asks every master hears it
// from each follower before the leader answers. That page is not an unreadable
// payload: it means "ask the leader", and the probe moves on quietly.

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
