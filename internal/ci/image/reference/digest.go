package reference

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"

	"goodkind.io/tack/internal/ci/image/environment"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const digestDirectoryPermBits = 0o777

type imageError string

func (e imageError) Error() string {
	return string(e)
}

const (
	errUnsupportedArchitecture = imageError("Unsupported image architecture")
	errNotDigest               = imageError("Build output is not a SHA256 image digest")
)

// IsDigest reports whether value is a lowercase sha256 image digest.
//
// A valid value starts with "sha256:" and ends with exactly 64 characters
// from "a" through "f" or "0" through "9".
//
//	IsDigest("sha256:" + strings.Repeat("a", 64)) // true
//	IsDigest("sha256:abc")                       // false
func IsDigest(value string) bool {
	return digestPattern.MatchString(value)
}

// WriteDigest writes IMAGE_DIGEST to a file in DIGEST_DIRECTORY.
//
// The function reads IMAGE_ARCH and IMAGE_DIGEST and creates
// DIGEST_DIRECTORY if needed. It accepts amd64 or arm64 and requires sha256:
// followed by 64 lowercase hexadecimal digits. It validates the architecture
// and digest before writing the digest followed by a newline.
//
// For IMAGE_ARCH=amd64, the file path is:
//
//	DIGEST_DIRECTORY/amd64.digest
//
// For IMAGE_ARCH=arm64, the file path is:
//
//	DIGEST_DIRECTORY/arm64.digest
func WriteDigest() error {
	architecture, err := environment.Required("IMAGE_ARCH")
	if err != nil {
		return &os.PathError{Op: "read image architecture", Path: "IMAGE_ARCH", Err: err}
	}

	if architecture != "amd64" && architecture != "arm64" {
		slog.Error(
			"reference.architecture_rejected",
			slog.String("architecture", architecture),
			slog.Any("error", errUnsupportedArchitecture),
		)

		return fmt.Errorf("%w: %s", errUnsupportedArchitecture, architecture)
	}

	value, err := environment.Required("IMAGE_DIGEST")
	if err != nil {
		return &os.PathError{Op: "read image digest", Path: "IMAGE_DIGEST", Err: err}
	}

	if !digestPattern.MatchString(value) {
		return errNotDigest
	}

	directory, err := environment.Required("DIGEST_DIRECTORY")
	if err != nil {
		return &os.PathError{Op: "read digest directory", Path: "DIGEST_DIRECTORY", Err: err}
	}

	err = os.MkdirAll(directory, digestDirectoryPermBits)
	if err != nil {
		return &os.PathError{Op: "create digest directory", Path: directory, Err: err}
	}

	return writeFile(directory+string(os.PathSeparator)+architecture+".digest", value+"\n")
}
