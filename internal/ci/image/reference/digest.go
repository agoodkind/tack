package reference

import (
	"fmt"
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

// WriteDigest validates the architecture and SHA256 digest, then writes
// the digest to DIGEST_DIRECTORY in a file named after IMAGE_ARCH.
func WriteDigest() error {
	architecture, err := environment.Required("IMAGE_ARCH")
	if err != nil {
		return &os.PathError{Op: "read image architecture", Path: "IMAGE_ARCH", Err: err}
	}

	if architecture != "amd64" && architecture != "arm64" {
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
