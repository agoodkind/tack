package ops

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// The reference-repair shape is the state production held the moment before
// the 2026-08-07 reference repair ran: one org, one workspace, every scope
// that carried issues, and every issue whose reference that repair renamed,
// counted, or keyed. Writing that shape onto a testbed is what lets the repair
// and its ledger reconstruction be proven there on generated data instead of
// only against production history (TACK-447).

const (
	// referenceShapeIssueTarget is how many issues the shape carries. The
	// repair wrote one reference key per issue, and the reconstruction counts
	// those keys, so the issue count and the recorded key count are the same
	// number.
	referenceShapeIssueTarget = recordedReferenceKeys + recordedFollowupReferenceKey
	// referenceShapeQuietHighWater is the sequence carried by the issue in each
	// scope the rename evidence never names. Those scopes exist so the scope
	// count the reconstruction derives matches the count the repair recorded.
	referenceShapeQuietHighWater = 25
)

// referenceShapeQuietProjects name the scopes that held issues but no
// colliding reference. The shape takes as many as the recorded counter-seed
// count needs beyond the scopes the rename evidence already names.
var referenceShapeQuietProjects = []string{"AGATE", "ICT", "LMS", "GK", "DOT", "NAS"}

// referenceShapeEpoch is the creation time every node in the shape carries. It
// sits before the repair window, so the whole corpus counts toward the classes
// the reconstruction derives. It also sits before the oldest identifier in the
// rename evidence, so a generated node always sorts below the evidence node it
// collides with and the repair's keep-oldest policy renames the evidence node,
// which is what production did.
var referenceShapeEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

var referenceShapeNamespace = uuid.MustParse("6f0a1f8c-6e2b-4f2a-9b3d-2c5a7d4e91b0")

// referenceShapeProject is one scope in the shape. HighWater is the largest
// sequence the scope carries, which is the value the repair seeds its counter
// to and therefore the value that decides which references the repair hands
// out when it renames a collision.
type referenceShapeProject struct {
	Identifier string
	ID         uuid.UUID
	HighWater  int
}

// referenceShapeIssue is one issue in the shape. Colliding marks an issue the
// repair must rename, so a report can separate the bad data from the corpus
// it sits in.
type referenceShapeIssue struct {
	ID        uuid.UUID
	Project   string
	Sequence  int
	Colliding bool
}

// referenceShapeGroup is one reference two or more issues rendered. Renamed
// holds the issues the repair moves off it, in the order the repair renames
// them, so the reference each one receives is predictable.
type referenceShapeGroup struct {
	Reference string
	Project   string
	Sequence  int
	Renamed   []uuid.UUID
}

// referenceShape is the whole corpus one run writes.
type referenceShape struct {
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	Projects    []referenceShapeProject
	Issues      []referenceShapeIssue
	Groups      []referenceShapeGroup
	Renames     int
}

// referenceShapeReference renders the reference one scope and sequence
// produce, which is the value the repair sees as a collision.
func referenceShapeReference(project string, sequence int) string {
	return project + "-" + strconv.Itoa(sequence)
}

// referenceShapeNodeID returns the identifier for one generated node: a UUID
// version 7 whose timestamp is the shape epoch and whose remaining bytes come
// from the key, so every run writes the same identifiers and a re-run rewrites
// the same corpus rather than a second copy of it.
func referenceShapeNodeID(kind, key string) uuid.UUID {
	digest := sha256.Sum256([]byte(kind + "\x00" + key))
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(referenceShapeEpoch.UnixMilli()))
	var raw [16]byte
	copy(raw[0:6], stamp[2:8])
	copy(raw[6:16], digest[0:10])
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	return uuid.UUID(raw)
}
