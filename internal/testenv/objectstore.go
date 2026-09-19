package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

const (
	// objectStoreImage is the SeaweedFS release production runs. The object
	// store is not a stack service: the configs repo runs it as a guest of its
	// own and pins seaweedfs_version in
	// ansible/inventory/group_vars/seaweedfs_all.yml, so this tag moves with
	// that value.
	objectStoreImage = "chrislusf/seaweedfs:4.45"
	// objectStorePort is the engine's S3 port.
	objectStorePort = "8333"
	// objectStoreConfigPath is where the engine reads its S3 identities, the
	// path production's service passes as -s3.config.
	objectStoreConfigPath = "/etc/seaweedfs/s3.json"
	// objectStoreRegion is the region requests are signed for. SeaweedFS
	// accepts any region; the signature needs one.
	objectStoreRegion = "us-east-1"
	// objectStoreWriterName and objectStoreReaderName name the engine's two
	// identities.
	objectStoreWriterName = "tack-testenv"
	objectStoreReaderName = "tack-testenv-read-only"
	// objectStoreKeyBytes is the random size of each generated key.
	objectStoreKeyBytes = 16
)

// objectStoreCommand is the weed invocation production's service runs, with
// the data directory at the image's volume.
var objectStoreCommand = []string{
	"server", "-dir=/data", "-s3", "-s3.config=" + objectStoreConfigPath,
	"-ip.bind=::", "-master.volumeSizeLimitMB=1024", "-volume.max=0",
}

// ObjectStoreBucket is one empty bucket on a test object store and what a
// client needs to reach it.
type ObjectStoreBucket struct {
	// Endpoint is the engine's S3 URL. Requests must use path-style
	// addressing, as they must against production's SeaweedFS.
	Endpoint string
	Region   string
	Bucket   string
	// AccessKey and SecretKey sign as the identity that may read and write
	// every bucket.
	AccessKey string
	SecretKey string
	// ReadOnlyAccessKey and ReadOnlySecretKey sign as an identity that may
	// read and list, and whose writes the engine refuses with AccessDenied.
	ReadOnlyAccessKey string
	ReadOnlySecretKey string
	// Container names the engine's container, for [StopObjectStore] and
	// [StartObjectStore].
	Container string
}

// objectStoreConfig is the S3 identity file the engine reads.
type objectStoreConfig struct {
	Identities []objectStoreIdentity `json:"identities"`
}

// objectStoreIdentity is one identity of the S3 identity file.
type objectStoreIdentity struct {
	Name        string                  `json:"name"`
	Credentials []objectStoreCredential `json:"credentials"`
	Actions     []string                `json:"actions"`
}

// objectStoreCredential is one key pair of an identity.
type objectStoreCredential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

var objectStoreState provisioned

// ObjectStore returns a new empty bucket on this process's SeaweedFS engine.
// Every call creates its own bucket, so tests sharing the engine share no
// objects.
func ObjectStore(t T) ObjectStoreBucket {
	t.Helper()
	skipWhenShort(t)
	containerName := objectStoreState.get(t, provisionObjectStore)
	bucket, err := newObjectStoreBucket(t.Context(), containerName)
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
	return bucket
}

// provisionObjectStore starts this process's engine with a generated pair of
// identities and returns its container name once it accepts writes.
func provisionObjectStore(ctx context.Context) (string, error) {
	identities, err := newObjectStoreConfig(ctx)
	if err != nil {
		return "", err
	}
	contents, err := json.Marshal(identities)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.config_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("render the object store identities: %w", err)
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	started, err := startEngine(ctx, cli, engineSpec{
		kind:     "seaweedfs",
		image:    objectStoreImage,
		platform: nil,
		cmd:      objectStoreCommand,
		env:      nil,
		files:    map[string][]byte{objectStoreConfigPath: contents},
	})
	if err != nil {
		return "", err
	}
	if _, err := waitForObjectStore(ctx, cli, started.name); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.objectstore.ready", slog.String("container", started.name))
	return started.name, nil
}

// newObjectStoreConfig generates the engine's identities: one that may do
// anything, and one that may only read and list.
func newObjectStoreConfig(ctx context.Context) (objectStoreConfig, error) {
	keys := make([]string, 0, 4)
	for range 4 {
		key, err := randomHex(ctx, objectStoreKeyBytes)
		if err != nil {
			return objectStoreConfig{}, err
		}
		keys = append(keys, key)
	}
	return objectStoreConfig{Identities: []objectStoreIdentity{
		{
			Name:        objectStoreWriterName,
			Credentials: []objectStoreCredential{{AccessKey: keys[0], SecretKey: keys[1]}},
			Actions:     []string{"Admin", "Read", "Write", "List"},
		},
		{
			Name:        objectStoreReaderName,
			Credentials: []objectStoreCredential{{AccessKey: keys[2], SecretKey: keys[3]}},
			Actions:     []string{"Read", "List"},
		},
	}}, nil
}
