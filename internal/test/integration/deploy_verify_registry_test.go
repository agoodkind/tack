package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// registryDescriptor is the OCI descriptor shape the registry API reads.
type registryDescriptor struct {
	MediaType string            `json:"mediaType"`
	Digest    string            `json:"digest"`
	Size      int               `json:"size"`
	Platform  *ocispec.Platform `json:"platform,omitempty"`
}

// localRegistry pushes blobs and manifests to a registry over HTTP.
type localRegistry struct {
	base string
	http *http.Client
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pushBlob uploads data in one monolithic upload and returns its descriptor.
func (r localRegistry) pushBlob(t *testing.T, repository, mediaType string, data []byte) registryDescriptor {
	t.Helper()
	digest := sha256Digest(data)
	start := r.send(t, http.MethodPost, r.base+"/v2/"+repository+"/blobs/uploads/", "", nil, http.StatusAccepted)
	location := start.Header.Get("Location")
	if !strings.HasPrefix(location, "http") {
		location = r.base + location
	}
	separator := "?"
	if strings.Contains(location, "?") {
		separator = "&"
	}
	r.send(t, http.MethodPut, location+separator+"digest="+digest, "application/octet-stream", data, http.StatusCreated)
	return registryDescriptor{MediaType: mediaType, Digest: digest, Size: len(data), Platform: nil}
}

// pushManifest uploads body under reference and returns its descriptor.
func (r localRegistry) pushManifest(t *testing.T, repository, reference, mediaType string, body []byte) registryDescriptor {
	t.Helper()
	r.send(t, http.MethodPut, r.base+"/v2/"+repository+"/manifests/"+reference, mediaType, body, http.StatusCreated)
	return registryDescriptor{MediaType: mediaType, Digest: sha256Digest(body), Size: len(body), Platform: nil}
}

func (r localRegistry) send(t *testing.T, method, url, contentType string, body []byte, want int) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := r.http.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != want {
		text, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("%s %s: status %d, want %d: %s", method, url, response.StatusCode, want, text)
	}
	return response
}

// logDeployImageReads logs the image fields the deploy check reads for one
// container: the daemon image store driver-type, the container Image, and the
// image Id, RepoDigests, and Descriptor. It reads no environment field.
func logDeployImageReads(t *testing.T, cli *client.Client, name string) {
	t.Helper()
	info, err := cli.Info(t.Context(), client.InfoOptions{})
	if err != nil {
		t.Fatalf("read docker info: %v", err)
	}
	inspected, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect container %s: %v", name, err)
	}
	imageID := inspected.Container.Image
	image, err := cli.ImageInspect(t.Context(), imageID)
	if err != nil {
		t.Fatalf("inspect image %s: %v", imageID, err)
	}
	t.Logf("Docker image fields read for %s: server=%s driver=%s driverStatus=%v container.Image=%s container.Config.Image=%s image.Id=%s repoDigests=%v descriptor=%s",
		name, info.Info.ServerVersion, info.Info.Driver, info.Info.DriverStatus, imageID, inspected.Container.Config.Image,
		image.ID, image.RepoDigests, mustMarshal(t, image.Descriptor))
}

// singleFileLayer returns a gzip tar layer with one file and its uncompressed
// digest, the diff ID of the layer.
func singleFileLayer(t *testing.T, content string) ([]byte, string) {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	header := &tar.Header{Name: "marker", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("write layer header: %v", err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatalf("write layer file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close layer: %v", err)
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err := zipper.Write(archive.Bytes()); err != nil {
		t.Fatalf("compress layer: %v", err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatalf("close compressed layer: %v", err)
	}
	return compressed.Bytes(), sha256Digest(archive.Bytes())
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %T: %v", value, err)
	}
	return encoded
}

// pushImageManifest pushes the image config and an image manifest for one
// architecture and returns the manifest descriptor. A non-empty reference
// stores the manifest under that tag; an empty one stores it under its digest.
func (r localRegistry) pushImageManifest(t *testing.T, repository, architecture string, layer registryDescriptor, diffID, reference string) registryDescriptor {
	t.Helper()
	config := mustMarshal(t, map[string]any{
		"architecture": architecture, "os": "linux",
		"config": map[string]any{"Cmd": []string{"/marker"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	})
	configDescriptor := r.pushBlob(t, repository, ocispec.MediaTypeImageConfig, config)
	manifest := mustMarshal(t, map[string]any{
		"schemaVersion": 2, "mediaType": ocispec.MediaTypeImageManifest,
		"config": configDescriptor, "layers": []registryDescriptor{layer},
	})
	if reference == "" {
		reference = sha256Digest(manifest)
	}
	return r.pushManifest(t, repository, reference, ocispec.MediaTypeImageManifest, manifest)
}

// pushSingleManifestImage pushes one image manifest for the host architecture,
// with no index, under tag and returns the manifest digest.
func (r localRegistry) pushSingleManifestImage(t *testing.T, repository, tag, marker string) string {
	t.Helper()
	layer, diffID := singleFileLayer(t, marker)
	layerDescriptor := r.pushBlob(t, repository, ocispec.MediaTypeImageLayerGzip, layer)
	pushed := r.pushImageManifest(t, repository, runtime.GOARCH, layerDescriptor, diffID, tag)
	t.Logf("pushed %s:%s as single manifest %s", repository, tag, pushed.Digest)
	return pushed.Digest
}
