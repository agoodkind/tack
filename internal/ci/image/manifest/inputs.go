// Package manifest publishes one multi-platform image index from native image digests.
package manifest

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"goodkind.io/tack/internal/ci/image/environment"
	"goodkind.io/tack/internal/ci/image/reference"
)

// architectures lists the source architectures in the order used for the index.
var architectures = []string{"amd64", "arm64"}

var errEmptyInput = errors.New("required environment variable is empty")

// inputs contains the validated values needed to publish the index.
type inputs struct {
	sources []name.Digest
	tags    []name.Tag
	auth    authn.Authenticator
}

// requiredValue returns a non-empty environment variable value.
func requiredValue(key string) (string, error) {
	value, err := environment.Required(key)
	if err != nil {
		return "", &os.PathError{Op: "read manifest input", Path: key, Err: err}
	}
	if value == "" {
		return "", &os.PathError{Op: "read manifest input", Path: key, Err: errEmptyInput}
	}
	return value, nil
}

// readInputs validates every input before the command contacts a registry.
func readInputs() (inputs, error) {
	var result inputs
	values := make(map[string]string)
	for _, key := range []string{
		"REGISTRY", "IMAGE_OWNER", "IMAGE_NAME", "IMAGE_TAG_FILE",
		"DIGEST_DIRECTORY", "GITHUB_ACTOR", "GITHUB_TOKEN",
	} {
		value, err := requiredValue(key)
		if err != nil {
			return result, err
		}
		values[key] = value
	}
	tags, err := readTags(values["IMAGE_TAG_FILE"])
	if err != nil {
		return result, err
	}
	repository := values["REGISTRY"] + "/" + values["IMAGE_OWNER"] + "/" + values["IMAGE_NAME"]
	sources, err := readSources(repository, values["DIGEST_DIRECTORY"])
	if err != nil {
		return result, err
	}
	result.tags = tags
	result.sources = sources
	result.auth = authn.FromConfig(authn.AuthConfig{
		Username: values["GITHUB_ACTOR"],
		Password: values["GITHUB_TOKEN"],
	})
	return result, nil
}

// readTags parses each non-empty line of the tag file as an image tag reference.
func readTags(path string) ([]name.Tag, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, &os.PathError{Op: "read image tag file", Path: path, Err: err}
	}
	var tags []name.Tag
	for line := range strings.Lines(string(content)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		tag, err := name.NewTag(line, name.StrictValidation)
		if err != nil {
			slog.Error("manifest.tag_parse_failed", slog.String("tag", line), slog.Any("error", err))
			return nil, fmt.Errorf("parse image tag %q: %w", line, err)
		}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return nil, errors.New("image manifest requires at least one tag")
	}
	return tags, nil
}

// readSources reads each architecture digest file and returns the source references in order.
func readSources(repository, directory string) ([]name.Digest, error) {
	var sources []name.Digest
	for _, architecture := range architectures {
		path := directory + string(os.PathSeparator) + architecture + ".digest"
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, &os.PathError{Op: "read image digest file", Path: path, Err: err}
		}
		digest := strings.TrimRight(string(content), "\n")
		if !reference.IsDigest(digest) {
			return nil, fmt.Errorf("invalid %s image digest", architecture)
		}
		source, err := name.NewDigest(repository+"@"+digest, name.StrictValidation)
		if err != nil {
			slog.Error("manifest.source_parse_failed", slog.String("architecture", architecture), slog.String("digest", digest), slog.Any("error", err))
			return nil, fmt.Errorf("parse %s image source: %w", architecture, err)
		}
		sources = append(sources, source)
	}
	return sources, nil
}
