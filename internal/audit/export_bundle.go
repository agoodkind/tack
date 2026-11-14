// export_bundle.go defines bundle file names and resolves the rows file for
// export writers and verifiers.

package audit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	// exportManifestFile is the fixed, mutable bundle manifest. Publishing
	// replaces this file in a single rename.
	exportManifestFile = "manifest.json"

	// exportEventsPrefix and exportEventsSuffix delimit the export ID in each
	// rows file name. Each export writes a unique rows file rather than
	// truncating or replacing another export's rows.
	exportEventsPrefix = "events-"
	exportEventsSuffix = ".jsonl"

	// legacyExportEventsFile is the rows file for manifests that predate
	// EventsFile and do not specify a rows file.
	legacyExportEventsFile = "events.jsonl"
)

// stagedSuffix marks an unfinished file outside the published bundle.
const stagedSuffix = ".partial"

// exportEventsFileName returns the rows file name for one export.
func exportEventsFileName(exportID uuid.UUID) string {
	return exportEventsPrefix + exportID.String() + exportEventsSuffix
}

// stagedExportPath returns a unique staging path for one export. The export
// ID prevents concurrent exports from truncating or publishing each other's
// staged manifest.
func stagedExportPath(publishedPath string, exportID uuid.UUID) string {
	return publishedPath + "." + exportID.String() + stagedSuffix
}

// bundleEventsFileName resolves the rows file specified by a signed manifest.
// Older manifests omit EventsFile and require legacyExportEventsFile. A change
// to EventsFile changes the signed payload and fails verification.
//
// The file name must not specify a path outside the bundle directory. The
// verifier opens the rows file before reporting its signature verdict and
// must not read an arbitrary path from untrusted input. The open operation
// separately checks the file type inside the directory.
func bundleEventsFileName(manifest ExportManifest) (string, error) {
	if manifest.EventsFile == "" {
		return legacyExportEventsFile, nil
	}

	name := manifest.EventsFile
	if name == "." || name == ".." || name != filepath.Base(name) ||
		strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf(
			"verify manifest: events_file %q is not a file name inside the bundle",
			name,
		)
	}

	return name, nil
}

// exporterOwnedFile recognizes only exporter file names for reclamation.
// It includes legacy rows, per-export rows, and staged manifests and rows,
// but excludes the published manifest. Per-export file names require a
// canonical UUID, not just a matching prefix and suffix. Reclamation must
// preserve operator files such as events-before-the-incident.jsonl.
func exporterOwnedFile(name string) bool {
	if name == legacyExportEventsFile {
		return true
	}

	if afterPrefix, hasPrefix := strings.CutPrefix(name, exportEventsPrefix); hasPrefix {
		exportID, hasSuffix := strings.CutSuffix(afterPrefix, exportEventsSuffix)
		return hasSuffix && isExportIDText(exportID)
	}

	stagedName, isStaged := strings.CutSuffix(name, stagedSuffix)
	if !isStaged {
		return false
	}

	return stagedForExport(stagedName, exportManifestFile) ||
		stagedForExport(stagedName, legacyExportEventsFile)
}

// stagedForExport reports whether stagedName consists of base followed by
// the export ID.
func stagedForExport(stagedName, base string) bool {
	exportID, found := strings.CutPrefix(stagedName, base+".")
	return found && isExportIDText(exportID)
}

// isExportIDText requires the canonical UUID spelling the exporter writes.
// uuid.Parse also accepts URNs, braces, omitted hyphens, and uppercase hex.
// The exporter does not write those forms and must not remove their files.
func isExportIDText(text string) bool {
	parsed, err := uuid.Parse(text)
	if err != nil {
		return false
	}

	return parsed.String() == text
}
