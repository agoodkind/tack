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

// Layer builds an image layer from archive entries.
//
// Layer writes each entry to a tar archive and fails the test if it cannot
// write the archive or build the layer. Regular file entries use Content
// as their file contents.
//
// A regular file entry can use these fields:
//
//	Entry{Name: "example.txt", Type: tar.TypeReg, Content: "example"}
func Layer(t *testing.T, entries ...Entry) v1.Layer {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Name, Typeflag: entry.Type, Linkname: entry.Linkname, Mode: 0o644}
		if entry.Type == tar.TypeReg {
			header.Size = int64(len(entry.Content))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write archive header %s: %v", entry.Name, err)
		}
		if entry.Type == tar.TypeReg {
			if _, err := io.WriteString(writer, entry.Content); err != nil {
				t.Fatalf("write archive content %s: %v", entry.Name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	content := buffer.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	if err != nil {
		t.Fatalf("build layer: %v", err)
	}
	return layer
}

// Image builds an image from layers for the specified platform.
//
// Image sets the image operating system and architecture from platform,
// then appends the layers. It fails the test if either operation fails.
//
// A platform can specify:
//
//	v1.Platform{OS: "linux", Architecture: "amd64"}
func Image(t *testing.T, platform v1.Platform, layers ...v1.Layer) v1.Image {
	t.Helper()
	base, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: platform.OS, Architecture: platform.Architecture})
	if err != nil {
		t.Fatalf("set image configuration: %v", err)
	}
	image, err := mutate.AppendLayers(base, layers...)
	if err != nil {
		t.Fatalf("append layers: %v", err)
	}
	return image
}

// PushIndex writes an OCI index to a tag and returns the index.
//
// PushIndex appends the addenda as children of an empty OCI index. It fails
// the test if it cannot parse the tag or write the index.
//
// It constructs the index with:
//
//	mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex), addenda...)
func PushIndex(t *testing.T, tag string, addenda ...mutate.IndexAddendum) v1.ImageIndex {
	t.Helper()
	index := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex), addenda...)
	reference, err := name.NewTag(tag)
	if err != nil {
		t.Fatalf("parse tag %s: %v", tag, err)
	}
	if err := remote.WriteIndex(reference, index); err != nil {
		t.Fatalf("write index %s: %v", tag, err)
	}
	return index
}
