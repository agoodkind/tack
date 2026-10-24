package ops_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

type retentionObjectStore struct {
	t       *testing.T
	bucket  testenv.ObjectStoreBucket
	client  *s3.Client
	stopped bool
}

func (store *retentionObjectStore) put(key string, body []byte) {
	store.t.Helper()
	if _, err := store.client.PutObject(store.t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(store.bucket.Bucket), Key: aws.String(key), Body: bytes.NewReader(body),
	}); err != nil {
		store.t.Fatalf("write retention object %s: %v", key, err)
	}
}

func (store *retentionObjectStore) remove(key string) {
	store.t.Helper()
	if _, err := store.client.DeleteObject(store.t.Context(), &s3.DeleteObjectInput{
		Bucket: aws.String(store.bucket.Bucket), Key: aws.String(key),
	}); err != nil {
		store.t.Fatalf("remove retention fixture object %s: %v", key, err)
	}
}

func (store *retentionObjectStore) object(key string) ([]byte, bool) {
	store.t.Helper()
	result, err := store.client.GetObject(store.t.Context(), &s3.GetObjectInput{Bucket: aws.String(store.bucket.Bucket), Key: aws.String(key)})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchKey" {
			return nil, false
		}
		store.t.Fatalf("read retention object %s: %v", key, err)
	}
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		store.t.Fatal(err)
	}
	return body, true
}

func (store *retentionObjectStore) signAsReader(cfg *config.Config) {
	cfg.BackupS3AccessKey = store.bucket.ReadOnlyAccessKey
	cfg.BackupS3SecretKey = store.bucket.ReadOnlySecretKey
}

func (store *retentionObjectStore) stop() {
	testenv.StopObjectStore(store.t, store.bucket.Container)
	store.stopped = true
}

func (store *retentionObjectStore) start() string {
	store.bucket.Endpoint = testenv.StartObjectStore(store.t, store.bucket.Container)
	store.stopped = false
	return store.bucket.Endpoint
}

func (store *retentionObjectStore) ensureStarted() {
	if store.stopped {
		store.start()
	}
}
