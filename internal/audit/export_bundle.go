// The bundle writer and verifier share the filenames and resolution rules for a
// manifest and its rows file.

package audit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	// exportManifestFile is the fixed, mutable filename in a bundle directory. Each
	// export uses its own rows filename and publishes the bundle by replacing the
	// manifest with one rename.
	exportManifestFile = "manifest.json"

	// exportEventsPrefix and exportEventsSuffix surround the export ID in a rows
	// filename. Each export writes its own file without truncating or renaming
	// another export's rows. Its manifest selects that file.
	exportEventsPrefix = "events-"
	exportEventsSuffix = ".jsonl"

	// legacyExportEventsFile is the fixed rows filename that older bundles used
	// before manifests included EventsFile. An older manifest without that field
	// uses this filename.
	legacyExportEventsFile = "events.jsonl"
)

// stagedSuffix identifies an incomplete file outside the published bundle. The
// bundle consists only of its manifest and the selected rows file.
const stagedSuffix = ".partial"

// exportEventsFileName returns one export's rows filename.
func exportEventsFileName(exportID uuid.UUID) string {
	return exportEventsPrefix + exportID.String() + exportEventsSuffix
}

// stagedExportPath returns the manifest path an export uses before publication.
//
// The export ID separates simultaneous writers. A shared staged path would let
// one export truncate another's manifest and publish the wrong bytes.
func stagedExportPath(publishedPath string, exportID uuid.UUID) string {
	return publishedPath + "." + exportID.String() + stagedSuffix
}

// bundleEventsFileName selects the rows file inside a bundle directory. The
// filename belongs to the signed manifest; changing it invalidates
// verification.
//
// Older manifests without EventsFile use their format's fixed filename. A
// change that adds the field to an older manifest or deletes it from a newer
// one changes the signing bytes. An unsigned filename cannot select rows for an
// accepted bundle.
//
// The function accepts only a plain filename inside the bundle directory. The
// verifier scans rows even when signature verification fails, before returning
// the complete report. Filename validation restricts the path; open-time checks
// restrict the entry type.
func bundleEventsFileName(manifest ExportManifest) (string, error) {
	if manifest.EventsFile == "" {
		return legacyExportEventsFile, nil
	}
	name := manifest.EventsFile
	if name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("verify manifest: events_file %q is not a file name inside the bundle", name)
	}
	return name, nil
}

// exporterOwnedFile reports whether a filename matches an exporter's output.
// Reclaim considers only matching names. An operator file using an exporter
// filename can still match.
//
// The function recognizes current per-export rows, the older fixed rows name,
// staged manifests, and staged rows from earlier revisions. It excludes the
// published manifest. Current and staged names require a canonical export ID;
// the older fixed rows name does not contain one.
//
// Earlier prefix-and-suffix matching also selected operator files such as
// events-before-the-incident.jsonl. Requiring the canonical ID excludes that
// example from reclamation.
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

// stagedForExport reports whether a staged name is base followed by the export
// ID that created it.
func stagedForExport(stagedName, base string) bool {
	exportID, found := strings.CutPrefix(stagedName, base+".")
	return found && isExportIDText(exportID)
}

// isExportIDText reports whether text matches the exporter's UUID rendering.
//
// [uuid.Parse] also accepts URN, braced, undashed, and uppercase hexadecimal
// forms. The exporter does not write those forms and must not reclaim their
// filenames.
func isExportIDText(text string) bool {
	parsed, err := uuid.Parse(text)
	if err != nil {
		return false
	}
	return parsed.String() == text
}
