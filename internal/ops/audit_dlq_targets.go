package ops

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// AuditReadTargets uses the digest of each summary group's error key to
// identify the group.
//
// The digest supplies the resource URI suffix for the group's audit target.
//
//   - The method returns one target for each ByError group.
func (result auditDLQInspectResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(result.ByError))
	for _, group := range result.ByError {
		digest := sha256.Sum256([]byte(group.Error))
		targets = append(
			targets,
			audit.Target{
				Kind:         audit.TargetKindResource,
				NodeType:     "",
				Reference:    "",
				Name:         "",
				URI:          "tack:audit/dlq/summary/" + hex.EncodeToString(digest[:]),
				RelationType: "",
				ID:           uuid.Nil,
				SourceID:     uuid.Nil,
				TargetID:     uuid.Nil,
			},
		)
	}

	return targets, nil
}
