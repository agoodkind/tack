// Package overlay checks vendored container overlays against pinned upstream images and patches.
//
// Each overlay replaces one file in a pinned upstream image (see docker-compose.yml). The check
// reads the upstream file from the linux/amd64 image, compares its sha256 with the recorded
// value, applies the committed patch, and compares the result with the committed overlay.
// The upstream hash replaces the upstream file itself because the upstream yugabyted file is
// large and its default constants trip secret scanners (TACK-302). The tags come from
// FDB_VERSION in the Makefile and the x-yugabyte-image anchor in docker-compose.yml.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"goodkind.io/tack/internal/ci/image/compose"
)

// registryVariable optionally sets the registry host that serves the upstream images.
// CI leaves it unset and reads from Docker Hub.
const registryVariable = "UPSTREAM_REGISTRY"

var errDrift = errors.New("overlay drift check failed; see diagnostics above and TACK-302")

// entries returns the overlays that the check covers.
func entries() ([]entry, error) {
	version, err := fdbVersion()
	if err != nil {
		return nil, err
	}
	yugabyteImage, err := compose.YugabyteImage(composePath)
	if err != nil {
		slog.Error("overlay.compose_image_failed", slog.String("path", composePath), slog.Any("error", err))
		return nil, fmt.Errorf("read pinned Yugabyte image from %s: %w", composePath, err)
	}
	return []entry{
		{
			image:     "foundationdb/foundationdb:" + version,
			imagePath: "/var/fdb/scripts/fdb.bash",
			overlay:   "fdb-overlay/fdb.bash",
			shaFile:   "fdb-overlay/fdb.bash.upstream.sha256",
			patch:     "fdb-overlay/fdb.bash.patch",
		},
		{
			image:     yugabyteImage,
			imagePath: "/home/yugabyte/bin/yugabyted",
			overlay:   "yugabyte-overlay/yugabyted",
			shaFile:   "yugabyte-overlay/yugabyted.upstream.sha256",
			patch:     "yugabyte-overlay/yugabyted.patch",
		},
	}, nil
}

// Check verifies the committed overlays and writes progress and drift messages to log.
// It compares each upstream file hash with its recorded hash and compares the
// patched upstream file with the overlay. It returns an error if a check fails.
// On success, it writes: Overlay drift check passed: each upstream sha256 matches
// its recorded value, and each patched upstream file matches its overlay.
func Check(ctx context.Context, log io.Writer) error {
	list, err := entries()
	if err != nil {
		return err
	}
	failed := false
	for _, item := range list {
		if err := writeLine(log, fmt.Sprintf("==> %s  (from %s:%s)", item.overlay, item.image, item.imagePath)); err != nil {
			return err
		}
		problems, err := checkEntry(ctx, item)
		var extraction *extractError
		if errors.As(err, &extraction) {
			problems = []string{extraction.Error()}
		} else if err != nil {
			return err
		}
		for _, problem := range problems {
			failed = true
			if err := writeLine(log, problem); err != nil {
				return err
			}
		}
	}
	if failed {
		return errDrift
	}
	return writeLine(log, "Overlay drift check passed: each upstream sha256 matches its recorded value, and each patched upstream file matches its overlay.")
}

func writeLine(log io.Writer, message string) error {
	if _, err := fmt.Fprintln(log, message); err != nil {
		return &os.PathError{Op: "write overlay check message", Path: "stderr", Err: err}
	}
	return nil
}

// checkEntry returns the failure messages for one entry.
func checkEntry(ctx context.Context, item entry) ([]string, error) {
	var problems []string
	for _, file := range item.vendoredFiles() {
		if _, err := os.Stat(file); err != nil {
			problems = append(problems, "FAIL: cannot access vendored file "+file)
		}
	}
	if len(problems) > 0 {
		return problems, nil
	}
	upstream, err := upstreamFile(ctx, item)
	if err != nil {
		return nil, &extractError{item: item, err: err}
	}
	return verify(item, upstream)
}

// extractError reports that the pinned upstream file could not be read from the image.
type extractError struct {
	item entry
	err  error
}

func (e *extractError) Error() string {
	return "FAIL: could not extract " + e.item.imagePath + " from " + e.item.image + ": " + e.err.Error()
}

func (e *extractError) Unwrap() error {
	return e.err
}

// upstreamFile reads the pinned file from the linux/amd64 image with anonymous access.
func upstreamFile(ctx context.Context, item entry) ([]byte, error) {
	address := item.image
	registry := os.Getenv(registryVariable)
	if registry != "" {
		address = registry + "/" + item.image
	}
	reference, err := name.ParseReference(address)
	if err != nil {
		slog.Error("overlay.image_parse_failed", slog.String("image", address), slog.Any("error", err))
		return nil, fmt.Errorf("parse image %s: %w", address, err)
	}
	image, err := remote.Image(reference,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}),
	)
	if err != nil {
		slog.Error("overlay.image_pull_failed", slog.String("image", address), slog.Any("error", err))
		return nil, fmt.Errorf("pull image %s: %w", address, err)
	}
	return readFile(ctx, image, item.imagePath)
}
