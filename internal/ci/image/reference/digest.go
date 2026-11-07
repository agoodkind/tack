package reference

import (
	"fmt"
	"os"
	"regexp"

	"goodkind.io/tack/internal/ci/image/environment"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const digestDirectoryPermBits = 0o777

// WriteDigest validates the architecture and SHA256 digest, then writes the digest to DIGEST_DIRECTORY in a file named after IMAGE_ARCH.
func WriteDigest() error {
	architecture, err := environment.Required("IMAGE_ARCH")
	if err != nil {
		return &os.PathError{Op: "read image architecture", Path: "IMAGE_ARCH", Err: err}
	}
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("%s: %s", "Unsupported image architecture", architecture)
	}
	value, err := environment.Required("IMAGE_DIGEST")
	if err != nil {
		return &os.PathError{Op: "read image digest", Path: "IMAGE_DIGEST", Err: err}
	}
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%s", "Build output is not a SHA256 image digest")
	}
	directory, err := environment.Required("DIGEST_DIRECTORY")
	if err != nil {
		return &os.PathError{Op: "read digest directory", Path: "DIGEST_DIRECTORY", Err: err}
	}
	if err := os.MkdirAll(directory, digestDirectoryPermBits); err != nil {
		return &os.PathError{Op: "create digest directory", Path: directory, Err: err}
	}
	return writeFile(directory+string(os.PathSeparator)+architecture+".digest", value+"\n")
}
