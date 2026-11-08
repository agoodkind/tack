// Package overlay checks vendored container overlays against pinned
// upstream images and patches.
//
// Each overlay replaces one file in a pinned upstream image. The check
// reads the upstream file from the linux/amd64 image, compares its sha256
// with the recorded value, applies the committed patch, and compares the
// result with the committed overlay. The check uses the upstream hash in
// place of the upstream file because the upstream yugabyted file is large
// and its default constants trip secret scanners (TACK-302). The check reads
// FDB_VERSION from the Makefile for the FDB tag and uses
// upstream.YugabyteImage for the Yugabyte image.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"goodkind.io/tack/internal/ci/image/upstream"
)

// registryVariable optionally sets the registry host that serves the
// upstream images. CI leaves it unset and reads from Docker Hub.
const registryVariable = "UPSTREAM_REGISTRY"

const passedMessage = "Overlay drift check passed: each upstream sha256 matches its " +
	"recorded value, and each patched upstream file matches its overlay."

var errDrift = errors.New("overlay drift check failed; see diagnostics above and TACK-302")

// entries returns the overlays that the check covers for the given FDB
// version.
func entries(version string) []entry {
	return []entry{
		{
			image:     "foundationdb/foundationdb:" + version,
			imagePath: "/var/fdb/scripts/fdb.bash",
			overlay:   "fdb-overlay/fdb.bash",
			shaFile:   "fdb-overlay/fdb.bash.upstream.sha256",
			patch:     "fdb-overlay/fdb.bash.patch",
		},
		{
			image:     upstream.YugabyteImage,
			imagePath: "/home/yugabyte/bin/yugabyted",
			overlay:   "yugabyte-overlay/yugabyted",
			shaFile:   "yugabyte-overlay/yugabyted.upstream.sha256",
			patch:     "yugabyte-overlay/yugabyted.patch",
		},
	}
}

// Check verifies the committed overlays and writes progress and drift
// messages to log.
//
// Check compares each upstream file hash with its recorded hash and compares
// the patched upstream file with the overlay. It returns an error if a check
// fails.
//
// On success, Check writes:
//
//	Overlay drift check passed: each upstream sha256 matches its recorded
//	value, and each patched upstream file matches its overlay.
func Check(ctx context.Context, log io.Writer) error {
	version, err := fdbVersion()
	if err != nil {
		return err
	}

	list := entries(version)
	registry := os.Getenv(registryVariable)
	failed := false

	for _, item := range list {
		heading := fmt.Sprintf("==> %s  (from %s:%s)", item.overlay, item.image, item.imagePath)

		err = writeLine(log, heading)
		if err != nil {
			return err
		}

		problems, err := checkEntry(ctx, registry, item)

		extraction, isExtraction := errors.AsType[*extractError](err)
		if isExtraction {
			problems = []string{extraction.Error()}
		} else if err != nil {
			return err
		}

		for _, problem := range problems {
			failed = true

			err = writeLine(log, problem)
			if err != nil {
				return err
			}
		}
	}

	if failed {
		return errDrift
	}

	return writeLine(log, passedMessage)
}

func writeLine(log io.Writer, message string) error {
	_, err := fmt.Fprintln(log, message)
	if err != nil {
		return &os.PathError{Op: "write overlay check message", Path: "stderr", Err: err}
	}

	return nil
}

// checkEntry returns failure messages for one entry. The caller reads
// registry once from UPSTREAM_REGISTRY. An empty registry selects Docker
// Hub.
func checkEntry(ctx context.Context, registry string, item entry) ([]string, error) {
	var problems []string

	for _, file := range item.vendoredFiles() {
		_, err := os.Stat(file)
		if err != nil {
			problems = append(problems, "FAIL: cannot access vendored file "+file)
		}
	}

	if len(problems) > 0 {
		return problems, nil
	}

	upstreamContent, err := upstreamFile(ctx, registry, item)
	if err != nil {
		return nil, &extractError{item: item, err: err}
	}

	return verify(item, upstreamContent)
}
