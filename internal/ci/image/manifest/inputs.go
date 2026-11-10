// Package manifest publishes one multi-platform image index from native
// image digests.
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

// architectures lists the source architectures in the order used for the
// index.
var architectures = []string{"amd64", "arm64"}

var (
	errEmptyInput    = errors.New("required environment variable is empty")
	errNoTags        = errors.New("image manifest requires at least one tag")
	errInvalidDigest = errors.New("invalid image digest")
)

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
		return "", &environmentError{key: key, err: err}
	}

	if value == "" {
		return "", &environmentError{key: key, err: errEmptyInput}
	}

	return value, nil
}

// environmentInputs contains the required environment values for the
// manifest command: registry, owner, image name, tag file, digest
// directory, actor, and token.
type environmentInputs struct {
	registry        string
	owner           string
	image           string
	tagFile         string
	digestDirectory string
	actor           string
	token           string
}

// readEnvironment reads required values in a fixed order and returns an
// error for the first missing or empty value.
func readEnvironment() (environmentInputs, error) {
	var values environmentInputs

	for _, field := range []struct {
		key    string
		target *string
	}{
		{"REGISTRY", &values.registry},
		{"IMAGE_OWNER", &values.owner},
		{"IMAGE_NAME", &values.image},
		{"IMAGE_TAG_FILE", &values.tagFile},
		{"DIGEST_DIRECTORY", &values.digestDirectory},
		{"GITHUB_ACTOR", &values.actor},
		{"GITHUB_TOKEN", &values.token},
	} {
		value, err := requiredValue(field.key)
		if err != nil {
			return environmentInputs{}, err
		}

		*field.target = value
	}

	return values, nil
}

// readInputs validates every input before the command contacts a registry.
func readInputs() (inputs, error) {
	values, err := readEnvironment()
	if err != nil {
		return inputs{}, err
	}

	tags, err := readTags(values.tagFile)
	if err != nil {
		return inputs{}, err
	}

	repository := values.registry + "/" + values.owner + "/" + values.image

	sources, err := readSources(repository, values.digestDirectory)
	if err != nil {
		return inputs{}, err
	}

	credentials := authn.AuthConfig{Username: values.actor, Password: values.token}

	return inputs{
		sources: sources,
		tags:    tags,
		auth:    authn.FromConfig(credentials),
	}, nil
}

// readTags parses each non-empty line of the tag file as an image tag
// reference.
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
			slog.Error(
				"manifest.tag_parse_failed",
				slog.String("tag", line),
				slog.Any("error", err),
			)

			return nil, fmt.Errorf("parse image tag %q: %w", line, err)
		}

		tags = append(tags, tag)
	}

	if len(tags) == 0 {
		return nil, errNoTags
	}

	return tags, nil
}

// readSources reads each architecture digest file and returns the source
// references in order.
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
			return nil, fmt.Errorf("%w for %s", errInvalidDigest, architecture)
		}

		source, err := name.NewDigest(repository+"@"+digest, name.StrictValidation)
		if err != nil {
			slog.Error(
				"manifest.source_parse_failed",
				slog.String("architecture", architecture),
				slog.String("digest", digest),
				slog.Any("error", err),
			)

			return nil, fmt.Errorf("parse %s image source: %w", architecture, err)
		}

		sources = append(sources, source)
	}

	return sources, nil
}
