package imagetest

import (
	"archive/tar"
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const archiveEntryMode = 0o644

// StartRegistry starts a local OCI registry for a test and returns its address.
//
// The test closes the server during cleanup. The returned address omits
// the http:// prefix. One example address is:
//
//	127.0.0.1:<port>
func StartRegistry(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)

	return strings.TrimPrefix(server.URL, "http://")
}

// Entry is one archive entry in a test image layer.
type Entry struct {
	Name     string
	Type     byte
	Content  string
	Linkname string
}

// FixtureLayer wraps a v1.Layer returned by Layer.
//
// The embedded interface exposes all v1.Layer methods while Layer returns
// a concrete type.
//
//	layer := imagetest.Layer(t, entries...)
//	digest, err := layer.Digest()
type FixtureLayer struct {
	v1.Layer
}

// FixtureImage wraps a v1.Image returned by Image.
//
// The embedded interface exposes all v1.Image methods while Image returns
// a concrete type.
//
//	image := imagetest.Image(t, platform, layers...)
//	digest, err := image.Digest()
type FixtureImage struct {
	v1.Image
}

// Layer builds an image layer from archive entries.
//
// Layer writes each entry to a tar archive and fails the test if it cannot
// write the archive or build the layer. Regular file entries use Content
// as their file contents.
//
// A regular file entry can use these fields:
//
//	Entry{Name: "example.txt", Type: tar.TypeReg, Content: "example"}
func Layer(t *testing.T, entries ...Entry) FixtureLayer {
	t.Helper()

	var buffer bytes.Buffer

	writer := tar.NewWriter(&buffer)

	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.Name,
			Typeflag: entry.Type,
			Linkname: entry.Linkname,
			Mode:     archiveEntryMode,
		}
		if entry.Type == tar.TypeReg {
			header.Size = int64(len(entry.Content))
		}

		err := writer.WriteHeader(header)
		if err != nil {
			t.Fatalf("write archive header %s: %v", entry.Name, err)
		}

		if entry.Type == tar.TypeReg {
			_, err = io.WriteString(writer, entry.Content)
			if err != nil {
				t.Fatalf("write archive content %s: %v", entry.Name, err)
			}
		}
	}

	err := writer.Close()
	if err != nil {
		t.Fatalf("close archive: %v", err)
	}

	content := buffer.Bytes()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	if err != nil {
		t.Fatalf("build layer: %v", err)
	}

	return FixtureLayer{Layer: layer}
}

// Image builds an image from layers for the specified platform.
//
// Image sets the image operating system and architecture from platform,
// then appends the layers. It fails the test if either operation fails.
//
// A platform can specify:
//
//	v1.Platform{OS: "linux", Architecture: "amd64"}
func Image(t *testing.T, platform v1.Platform, layers ...v1.Layer) FixtureImage {
	t.Helper()

	configuration := &v1.ConfigFile{OS: platform.OS, Architecture: platform.Architecture}

	base, err := mutate.ConfigFile(empty.Image, configuration)
	if err != nil {
		t.Fatalf("set image configuration: %v", err)
	}

	image, err := mutate.AppendLayers(base, layers...)
	if err != nil {
		t.Fatalf("append layers: %v", err)
	}

	return FixtureImage{Image: image}
}

// PushedIndex stores the digest and parsed manifest of an index
// PushIndex wrote.
type PushedIndex struct {
	Digest   v1.Hash
	Manifest *v1.IndexManifest
}

// PushIndex writes an OCI image index to a registry tag.
//
// PushIndex adds the supplied entries to the index and returns its digest and
// parsed manifest. It fails the test if it cannot write or inspect the index.
//
// Add a platform image to an index:
//
//	pushed := imagetest.PushIndex(t, tag,
//		mutate.IndexAddendum{Add: platformImage, Platform: &platform},
//	)
func PushIndex(t *testing.T, tag string, addenda ...mutate.IndexAddendum) PushedIndex {
	t.Helper()

	index := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex), addenda...)

	reference, err := name.NewTag(tag)
	if err != nil {
		t.Fatalf("parse tag %s: %v", tag, err)
	}

	err = remote.WriteIndex(reference, index)
	if err != nil {
		t.Fatalf("write index %s: %v", tag, err)
	}

	digest, err := index.Digest()
	if err != nil {
		t.Fatalf("digest index %s: %v", tag, err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatalf("read index manifest %s: %v", tag, err)
	}

	return PushedIndex{Digest: digest, Manifest: manifest}
}
