package service

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func snapshotStepTarget(session searchdomain.Session, operation snapshotOperation) audit.Target {
	return audit.Target{
		Kind:         audit.TargetKindExternal,
		NodeType:     "",
		Name:         "",
		URI:          "",
		RelationType: "",
		Reference:    snapshotReferencePrefix + session.ID.String() + "/" + operation.action,
		ID:           uuid.Nil,
		OwnerID:      uuid.Nil,
		SourceID:     uuid.Nil,
		TargetID:     uuid.Nil,
	}
}
