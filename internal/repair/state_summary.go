package repair

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

func repairConfirmationToken(preview *RepairPreview, canonicalInput string) string {
	payload := strings.Join([]string{
		string(preview.Class),
		preview.NodeID.String(),
		preview.CurrentUpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		preview.RawState,
		canonicalInput,
		preview.WinnerStateID.String(),
	}, "|")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func summarizeStateRepairFailure(rawErr error, canonicalErr error) string {
	parts := []string{"raw state alias is present but no workflow state winner could be resolved"}
	if rawErr != nil {
		parts = append(parts, "raw="+rawErr.Error())
	}
	if canonicalErr != nil {
		parts = append(parts, "canonical="+canonicalErr.Error())
	}
	return strings.Join(parts, "; ")
}

func summarizeStateRepairSuccess(preview *RepairPreview, rawState *workflowStateCandidate, canonicalState *workflowStateCandidate) string {
	parts := []string{fmt.Sprintf("remove raw state alias and keep canonical state_id=%s", preview.WinnerStateName)}
	if rawState != nil {
		parts = append(parts, "raw_rank="+strconv.FormatInt(rawState.Rank, 10))
	}
	if canonicalState != nil {
		parts = append(parts, "canonical_rank="+strconv.FormatInt(canonicalState.Rank, 10))
	}
	return strings.Join(parts, "; ")
}
