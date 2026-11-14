package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

const (
	// objectStoreImage matches the production SeaweedFS release. The configs
	// repository runs SeaweedFS in a separate guest and pins seaweedfs_version
	// in ansible/inventory/group_vars/seaweedfs_all.yml. Update this tag with it.
	objectStoreImage = "chrislusf/seaweedfs:4.45"
	// objectStorePort is the engine's S3 port.
	objectStorePort = "8333"
	// objectStoreConfigPath specifies the S3 identity file that production
	// passes as -s3.config.
	objectStoreConfigPath = "/etc/seaweedfs/s3.json"
	// objectStoreRegion specifies the region for request signatures.
	// SeaweedFS accepts any region, but signatures require one.
	objectStoreRegion = "us-east-1"
	// objectStoreWriterName and objectStoreReaderName identify the two identities.
	objectStoreWriterName = "tack-testenv"
	objectStoreReaderName = "tack-testenv-read-only"
	// objectStoreVolumeCount is the engine's volume count.
	objectStoreVolumeCount = "512"
	// objectStoreHolderSeconds exceeds the lifetime of a test binary.
	objectStoreHolderSeconds = "2147483647"
	// objectStoreKeyBytes is the random size of each generated key.
	objectStoreKeyBytes = 16
)

// objectStoreCommand runs production's weed invocation with the image's data
// directory and test-specific volume allocation and restart settings.
// Each test creates a bucket with its own collection and volumes. The fixed
// volume limit permits these allocations without depending on runner disk size.
// SeaweedFS does not preallocate the volumes. Disabling master.resumeState
// speeds leader election after restart and preserves buckets and objects.
var objectStoreCommand = []string{
	"server",
	"-dir=/data",
	"-s3",
	"-s3.config=" + objectStoreConfigPath,
	"-ip.bind=::",
	"-master.volumeSizeLimitMB=1024",
	"-volume.max=" + objectStoreVolumeCount,
	"-master.resumeState=false",
}

// objectStoreEnv allocates one volume per new collection instead of seven.
var objectStoreEnv = []string{"WEED_MASTER_VOLUME_GROWTH_COPY_1=1"}

// ObjectStoreBucket specifies a test bucket and its connection credentials.
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
	// ReadOnlyAccessKey and ReadOnlySecretKey sign read and list requests.
	// The engine rejects writes from this identity with AccessDenied.
	ReadOnlyAccessKey string
	ReadOnlySecretKey string
	// Container identifies the engine for [StopObjectStore] and [StartObjectStore].
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
// Each call creates a separate bucket to isolate the test's objects.
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

// provisionObjectStore starts this process's engine with generated identities
// and returns its container name after a successful write. A separate sleeping
// container shares its network stack with the engine and reserves its address
// during outages. Stopped engines refuse connections at that reserved address.
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
	holder, err := startEngine(
		ctx,
		cli,
		engineSpec{
			kind:       "seaweedfs-address",
			image:      objectStoreImage,
			platform:   nil,
			entrypoint: []string{"sleep"},
			cmd:        []string{objectStoreHolderSeconds},
			env:        nil,
		},
	)
	if err != nil {
		return "", err
	}

	started, err := startEngine(
		ctx,
		cli,
		engineSpec{
			kind:      "seaweedfs",
			image:     objectStoreImage,
			platform:  nil,
			cmd:       objectStoreCommand,
			env:       objectStoreEnv,
			files:     map[string][]byte{objectStoreConfigPath: contents},
			networkOf: holder.name,
		},
	)
	if err != nil {
		return "", err
	}

	if _, err := waitForObjectStore(ctx, cli, started.name); err != nil {
		return "", err
	}

	slog.InfoContext(ctx, "testenv.objectstore.ready", slog.String("container", started.name))

	return started.name, nil
}

// newObjectStoreConfig generates an administrator identity and an identity
// that may only read and list.
func newObjectStoreConfig(ctx context.Context) (objectStoreConfig, error) {
	keys := make([]string, 0, 4)

	for range 4 {
		key, err := randomHex(ctx, objectStoreKeyBytes)
		if err != nil {
			return objectStoreConfig{}, err
		}

		keys = append(keys, key)
	}

	return objectStoreConfig{
		Identities: []objectStoreIdentity{
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
		},
	}, nil
}
