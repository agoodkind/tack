package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/moby/moby/client"
)

const (
	// objectStoreReadyBucket holds the object a readiness probe writes.
	objectStoreReadyBucket = "tack-testenv-ready"
	// objectStoreProbeTimeout bounds one readiness probe.
	objectStoreProbeTimeout = 30 * time.Second
	// objectStoreBucketBytes is the random size of a bucket name's suffix.
	objectStoreBucketBytes = 8
)

// newObjectStoreClient builds an S3 client signing as the writer identity,
// with the path-style addressing SeaweedFS requires.
func newObjectStoreClient(access ObjectStoreBucket) *s3.Client {
	signer := credentials.NewStaticCredentialsProvider(
		access.AccessKey,
		access.SecretKey,
		"",
	)
	return s3.NewFromConfig(aws.Config{
		Region:      access.Region,
		Credentials: signer,
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(access.Endpoint)
		options.UsePathStyle = true
	})
}

// newObjectStoreBucket creates an empty bucket with a random name on the
// engine in containerName.
func newObjectStoreBucket(ctx context.Context, containerName string) (ObjectStoreBucket, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	defer func() { _ = cli.Close() }()
	access, err := objectStoreAccess(ctx, cli, containerName)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	suffix, err := randomHex(ctx, objectStoreBucketBytes)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	access.Bucket = "tack-test-" + suffix
	if err := createObjectStoreBucket(ctx, newObjectStoreClient(access), access.Bucket); err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.bucket_failed", slog.String("err", err.Error()))
		return ObjectStoreBucket{}, err
	}
	return access, nil
}

// createObjectStoreBucket creates bucket unless this identity already owns it.
// It leaves logging to the caller, because a readiness probe expects it to
// fail while the engine starts.
func createObjectStoreBucket(ctx context.Context, s3Client *s3.Client, bucket string) error {
	_, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *s3types.BucketAlreadyOwnedByYou
	if err == nil || errors.As(err, &owned) {
		return nil
	}
	return errors.New("create bucket " + bucket + ": " + err.Error())
}

// waitForObjectStore polls until the engine in containerName stores an
// object, and returns how it is reached. An engine answers HTTP before its
// volume server has registered, so only a completed write proves it ready.
func waitForObjectStore(ctx context.Context, cli *client.Client, containerName string) (ObjectStoreBucket, error) {
	access, err := objectStoreAccess(ctx, cli, containerName)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	s3Client := newObjectStoreClient(access)
	for {
		lastErr := probeObjectStore(ctx, s3Client)
		if lastErr == nil {
			return access, nil
		}
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.objectstore.not_ready", slog.String("err", lastErr.Error()))
			return ObjectStoreBucket{}, fmt.Errorf("the test object store stored nothing before the deadline: %w", lastErr)
		}
	}
}

// probeObjectStore makes one bounded write. A failed probe is expected while
// the engine starts, so it logs at debug and returns the reason for the final
// deadline error.
func probeObjectStore(ctx context.Context, s3Client *s3.Client) error {
	probeCtx, cancel := context.WithTimeout(ctx, objectStoreProbeTimeout)
	defer cancel()
	if err := createObjectStoreBucket(probeCtx, s3Client, objectStoreReadyBucket); err != nil {
		slog.DebugContext(ctx, "testenv.objectstore.probe", slog.String("err", err.Error()))
		return err
	}
	_, err := s3Client.PutObject(probeCtx, &s3.PutObjectInput{
		Bucket: aws.String(objectStoreReadyBucket),
		Key:    aws.String("ready"),
		Body:   bytes.NewReader([]byte("ready")),
	})
	if err != nil {
		slog.DebugContext(ctx, "testenv.objectstore.probe", slog.String("err", err.Error()))
		return errors.New("write the readiness object: " + err.Error())
	}
	return nil
}

// objectStoreAccess reads a running or stopped engine's address and
// identities back from its container, so a process that did not start the
// engine can still reach it.
func objectStoreAccess(ctx context.Context, cli *client.Client, containerName string) (ObjectStoreBucket, error) {
	address, err := containerAddress(ctx, cli, containerName)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	contents, err := readContainerFile(ctx, cli, containerName, objectStoreConfigPath)
	if err != nil {
		return ObjectStoreBucket{}, err
	}
	var identities objectStoreConfig
	if err := json.Unmarshal(contents, &identities); err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.config_failed", slog.String("err", err.Error()))
		return ObjectStoreBucket{}, fmt.Errorf("parse %s in %s: %w", objectStoreConfigPath, containerName, err)
	}
	access := ObjectStoreBucket{
		Endpoint:          "http://" + net.JoinHostPort(address, objectStorePort),
		Region:            objectStoreRegion,
		Bucket:            "",
		AccessKey:         "",
		SecretKey:         "",
		ReadOnlyAccessKey: "",
		ReadOnlySecretKey: "",
		Container:         containerName,
	}
	writer, writerFound := identityCredential(identities, objectStoreWriterName)
	reader, readerFound := identityCredential(identities, objectStoreReaderName)
	if !writerFound || !readerFound {
		return ObjectStoreBucket{}, fmt.Errorf("%s in %s lacks the testenv identities", objectStoreConfigPath, containerName)
	}
	access.AccessKey, access.SecretKey = writer.AccessKey, writer.SecretKey
	access.ReadOnlyAccessKey, access.ReadOnlySecretKey = reader.AccessKey, reader.SecretKey
	return access, nil
}

// identityCredential returns the one key pair of the identity called name.
func identityCredential(identities objectStoreConfig, name string) (objectStoreCredential, bool) {
	for _, identity := range identities.Identities {
		if identity.Name == name && len(identity.Credentials) == 1 {
			return identity.Credentials[0], true
		}
	}
	return objectStoreCredential{AccessKey: "", SecretKey: ""}, false
}
