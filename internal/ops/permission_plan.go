package ops

import (
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func accessRolloutPlan(
	authorityID uuid.UUID,
	in searchRolloutInput,
	current searchdomain.AccessRollout,
) accessRolloutEvidence {
	return accessRolloutEvidence{
		AuthorityID:        authorityID.String(),
		CandidateVersion:   in.Candidate,
		ExpectedGeneration: current.Generation,
		ActiveVersion:      current.ActiveVersion,
		Phase:              "",
		WriteVersions:      nil,
		Generation:         0,
	}
}
