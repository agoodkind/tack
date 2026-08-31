// export_bundle.go names the two files a bundle is made of and resolves which
// rows file a manifest describes. Both halves of the bundle, the writer and the
// verifier, read their names from here.

package audit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	// exportManifestFile is the one name in a bundle directory that is fixed and
	// mutable. Everything else a bundle holds is named for the export that wrote
	// it, so publishing a bundle is the single rename that replaces this file.
	exportManifestFile = "manifest.json"

	// exportEventsPrefix and exportEventsSuffix bracket the export id in the
	// name of the file carrying a bundle's rows. The id makes the name unique,
	// so no two exports into one directory ever write, truncate, or rename the
	// same rows file, and a manifest can only ever name the rows its own export
	// wrote.
	exportEventsPrefix = "events-"
	exportEventsSuffix = ".jsonl"

	// legacyExportEventsFile is the fixed name every bundle used before the
	// manifest carried the name of its own rows. A manifest that names no events
	// file is one of those, and this is the name its format fixed.
	legacyExportEventsFile = "events.jsonl"
)

// stagedSuffix names a file that is being written and is not part of a bundle
// yet. A bundle is a manifest and the rows it names, so nothing carrying this
// suffix is ever mistaken for either.
const stagedSuffix = ".partial"

// exportEventsFileName is the name of the rows file one export writes.
func exportEventsFileName(exportID uuid.UUID) string {
	return exportEventsPrefix + exportID.String() + exportEventsSuffix
}

// stagedExportPath names the manifest one export writes before it publishes.
//
// The export id is in the name because two exports into one directory would
// otherwise create and truncate the same staged path, and the second one's
// write would land under the first one's rename.
func stagedExportPath(publishedPath string, exportID uuid.UUID) string {
	return publishedPath + "." + exportID.String() + stagedSuffix
}

// bundleEventsFileName resolves which file in the bundle directory a manifest
// describes. Verification reads this rather than a fixed path, which is what
// makes a manifest paired with another export's rows unrepresentable: the name
// is inside the signed manifest, so a manifest names its own rows or it does
// not verify.
//
// A manifest naming no events file is one written before the format carried the
// name, and the name its format fixed is returned for it. That fallback cannot
// be induced on a bundle written since: the name is covered by the signature, so
// stripping it from a manifest that had one breaks that manifest's signature,
// and adding one to a manifest that had none breaks its signature too. No name
// that is not signed can ever decide which rows are read.
//
// The name is refused unless it is a plain name inside the bundle directory.
// The manifest is untrusted input until its signature verifies, and the scan
// opens the file it names before that verdict is reported, so a manifest must
// not be able to point the verifier at a path of its choosing.
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

// exporterOwnedFile reports whether a name in a bundle directory is one this
// exporter writes. Reclaiming is confined to these, so an operator's own files
// in the directory are never candidates for removal, whatever they are called.
//
// Every name this exporter has ever written is covered: the per-export rows
// file, the fixed rows name bundles used before that, a staged manifest, and
// the staged rows name earlier revisions wrote. The published manifest is
// deliberately absent: it is the one file that is always the bundle.
func exporterOwnedFile(name string) bool {
	if name == legacyExportEventsFile {
		return true
	}
	if strings.HasPrefix(name, exportEventsPrefix) && strings.HasSuffix(name, exportEventsSuffix) {
		return true
	}
	if !strings.HasSuffix(name, stagedSuffix) {
		return false
	}
	return strings.HasPrefix(name, exportManifestFile+".") ||
		strings.HasPrefix(name, legacyExportEventsFile+".")
}
