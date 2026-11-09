package overlay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

var errPatchFileCount = errors.New("patch must change exactly one file")

// entry identifies one vendored overlay and the upstream file it replaces.
type entry struct {
	image     string
	imagePath string
	overlay   string
	shaFile   string
	patch     string
}

func (item entry) vendoredFiles() []string {
	return []string{item.overlay, item.shaFile, item.patch}
}

// verifyHash reports a drift message when the upstream bytes differ from
// the recorded sha256.
func verifyHash(item entry, upstream []byte) (string, error) {
	recorded, err := os.ReadFile(item.shaFile)
	if err != nil {
		return "", &os.PathError{Op: "read recorded sha256", Path: item.shaFile, Err: err}
	}

	want := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}

		return r
	}, string(recorded))
	sum := sha256.Sum256(upstream)
	got := hex.EncodeToString(sum[:])

	if want == got {
		return "", nil
	}

	return fmt.Sprintf("DRIFT: %s:%s changed (sha256 %s, recorded %s).\n"+
		"       The pinned image's upstream file moved. Re-vendor:\n"+
		"         1) extract the new upstream from the image\n"+
		"         2) re-apply the intended change to produce %s\n"+
		"         3) refresh the hash:  sha256sum <upstream> | cut -d' ' -f1 > %s\n"+
		"         4) refresh the patch: diff -u <upstream> %s > %s\n"+
		"         5) verify on QA before prod",
		item.image, item.imagePath, got, want,
		item.overlay, item.shaFile, item.overlay, item.patch), nil
}

// applyPatch applies the committed patch to the upstream bytes.
func applyPatch(patchPath string, upstream []byte) ([]byte, error) {
	file, err := os.Open(patchPath)
	if err != nil {
		return nil, &os.PathError{Op: "open patch", Path: patchPath, Err: err}
	}

	files, _, parseErr := gitdiff.Parse(file)
	closeErr := file.Close()

	if parseErr != nil {
		slog.Error(
			"overlay.patch_parse_failed",
			slog.String("path", patchPath),
			slog.Any("error", parseErr),
		)

		return nil, fmt.Errorf("parse patch %s: %w", patchPath, parseErr)
	}

	if closeErr != nil {
		return nil, &os.PathError{Op: "close patch", Path: patchPath, Err: closeErr}
	}

	if len(files) != 1 {
		slog.Error(
			"overlay.patch_file_count",
			slog.String("path", patchPath),
			slog.Int("files", len(files)),
			slog.Any("error", errPatchFileCount),
		)

		return nil, fmt.Errorf("patch %s changes %d files: %w", patchPath, len(files), errPatchFileCount)
	}

	var result bytes.Buffer

	err = gitdiff.Apply(&result, bytes.NewReader(upstream), files[0])
	if err != nil {
		slog.Error("overlay.patch_apply_failed", slog.String("path", patchPath), slog.Any("error", err))

		return nil, fmt.Errorf("apply patch %s: %w", patchPath, err)
	}

	return result.Bytes(), nil
}

// verifyPatch reports a drift message when the patch fails or rebuilds a
// different overlay.
func verifyPatch(item entry, upstream []byte) (string, error) {
	rebuilt, err := applyPatch(item.patch, upstream)
	if err != nil {
		return fmt.Sprintf("DRIFT: %s does not apply cleanly to upstream %s (%v). Regenerate it:\n"+
			"         diff -u <upstream> %s > %s",
			item.patch, item.imagePath, err, item.overlay, item.patch), nil
	}

	overlay, err := os.ReadFile(item.overlay)
	if err != nil {
		return "", &os.PathError{Op: "read overlay", Path: item.overlay, Err: err}
	}

	if !bytes.Equal(overlay, rebuilt) {
		return fmt.Sprintf("DRIFT: %s does not equal upstream + %s. Regenerate the patch:\n"+
			"         diff -u <upstream> %s > %s",
			item.overlay, item.patch, item.overlay, item.patch), nil
	}

	return "", nil
}

// verify returns the drift messages for one entry. It returns an error only
// for unreadable vendored files.
func verify(item entry, upstream []byte) ([]string, error) {
	var messages []string

	hashMessage, err := verifyHash(item, upstream)
	if err != nil {
		return nil, err
	}

	if hashMessage != "" {
		messages = append(messages, hashMessage)
	}

	patchMessage, err := verifyPatch(item, upstream)
	if err != nil {
		return nil, err
	}

	if patchMessage != "" {
		messages = append(messages, patchMessage)
	}

	return messages, nil
}
