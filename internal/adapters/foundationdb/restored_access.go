package foundationdb

import searchdomain "goodkind.io/tack/internal/domain/search"

func rollbackRecord(
	record searchRolloutRecord,
	request searchdomain.RollbackAccessRollout,
	restoredVersion string,
	generation int64,
) searchRolloutRecord {
	restored := record
	restored.ActiveVersion, restored.CandidateVersion = restoredVersion, restoredVersion
	restored.PreviousVersion, restored.WriteVersions = record.CandidateVersion, []string{
		restoredVersion,
	}
	restored.Phase, restored.ScanCursor, restored.VerifyCursor, restored.RetireCursor = string(
		searchdomain.AccessVerifying,
	), "", "", ""
	restored.ScanComplete, restored.AffectedPass, restored.Generation = false, false, generation
	restored.InitiatedOperationID, restored.InitiatedActorID = request.Initiation.OperationID, request.Initiation.ActorID

	return restored
}
