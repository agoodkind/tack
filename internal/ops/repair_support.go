package ops

// repairNeedDetails explains, in one line, whether a preview would change
// persisted props or found the reference property already correct.
func repairNeedDetails(preview *RepairPreview) string {
	if preview.NeedsRepair {
		return "repair would change persisted props"
	}
	return "reference property already matches selected storage"
}

// repairPreviewStatus maps a preview to a coarse status word used in output.
func repairPreviewStatus(preview *RepairPreview) string {
	if preview == nil {
		return "blocked"
	}
	if !preview.NeedsRepair {
		return "noop"
	}
	if preview.CanApply {
		return "ready"
	}
	return "blocked"
}
