package image_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// TestManifestPublishesEveryChildUnderEveryTag checks that every tag contains
// the source child descriptors and that each child image is readable.
func TestManifestPublishesEveryChildUnderEveryTag(t *testing.T) {
	fixture := newManifestFixture(t)

	output, err := runWithArguments(t, fixture.environment(), "manifest")
	if err != nil {
		t.Fatalf("err = %v, output = %s", err, output)
	}

	for _, tag := range fixture.tags {
		reference, err := name.ParseReference(tag)
		if err != nil {
			t.Fatalf("parse %s: %v", tag, err)
		}

		index, err := remote.Index(reference)
		if err != nil {
			t.Fatalf("read published index %s: %v", tag, err)
		}

		mediaType, err := index.MediaType()
		if err != nil || mediaType != types.OCIImageIndex {
			t.Fatalf("%s media type = %s, err = %v", tag, mediaType, err)
		}

		manifest, err := index.IndexManifest()
		if err != nil {
			t.Fatalf("read index manifest %s: %v", tag, err)
		}

		if !reflect.DeepEqual(manifest.Manifests, fixture.wantChilds) {
			t.Fatalf("%s children = %+v, want %+v", tag, manifest.Manifests, fixture.wantChilds)
		}

		for _, child := range manifest.Manifests {
			_, err = index.Image(child.Digest)
			if err != nil {
				t.Fatalf("%s child %s is not readable: %v", tag, child.Digest, err)
			}
		}
	}
}

// TestManifestRefusesBadInputBeforePublishing checks that invalid inputs
// fail without publishing tags or exposing the token.
func TestManifestRefusesBadInputBeforePublishing(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(fixture manifestFixture, environment []string) []string
	}{
		{"uppercase digest", func(fixture manifestFixture, environment []string) []string {
			writeDigest(t, fixture, "arm64", "sha256:"+strings.Repeat("A", 64))

			return environment
		}},
		{"missing digest file", func(fixture manifestFixture, environment []string) []string {
			err := os.Remove(filepath.Join(fixture.digestDir, "arm64.digest"))
			if err != nil {
				t.Fatalf("remove digest: %v", err)
			}

			return environment
		}},
		{"absent source image", func(fixture manifestFixture, environment []string) []string {
			writeDigest(t, fixture, "arm64", "sha256:"+strings.Repeat("b", 64))

			return environment
		}},
		{"empty tag file", func(fixture manifestFixture, environment []string) []string {
			writeTestFile(t, fixture.tagFile, "\n\n")

			return environment
		}},
		{"empty token", func(_ manifestFixture, environment []string) []string {
			kept := slices.DeleteFunc(environment, func(item string) bool {
				return strings.HasPrefix(item, tokenPrefix)
			})

			return append(kept, tokenPrefix)
		}},
		{"missing actor", func(_ manifestFixture, environment []string) []string {
			return slices.DeleteFunc(environment, func(item string) bool {
				return strings.HasPrefix(item, actorPrefix)
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManifestFixture(t)
			environment := test.mutate(fixture, fixture.environment())

			output, err := runWithArguments(t, environment, "manifest")
			if err == nil {
				t.Fatalf("expected failure, output = %s", output)
			}

			if strings.Count(output, "ci-image.failed") != 1 {
				t.Fatalf("expected one boundary log, output = %s", output)
			}

			if strings.Contains(output, "test-token") {
				t.Fatalf("output contains the token: %s", output)
			}

			for _, tag := range fixture.tags {
				reference, parseErr := name.ParseReference(tag)
				if parseErr != nil {
					t.Fatalf("parse %s: %v", tag, parseErr)
				}

				_, headErr := remote.Head(reference)
				if headErr == nil {
					t.Fatalf("tag %s exists after a refused publication", tag)
				}
			}
		})
	}
}
