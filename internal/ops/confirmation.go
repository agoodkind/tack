package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func repairConfirmationToken(preview *RepairPreview) string {
	plannedProps, err := json.Marshal(preview.PlannedProps)
	if err != nil {
		plannedProps = []byte(transformedNullProperty)
	}

	plannedRelationships, err := json.Marshal(preview.PlannedRelationships)
	if err != nil {
		plannedRelationships = []byte(transformedNullProperty)
	}

	payload := strings.Join(
		[]string{
			string(preview.Class),
			preview.ProfileName,
			preview.NodeID.String(),
			preview.CurrentUpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			preview.TargetProperty,
			string(plannedProps),
			string(plannedRelationships),
		},
		"|",
	)
	digest := sha256.Sum256([]byte(payload))

	return hex.EncodeToString(digest[:])
}
