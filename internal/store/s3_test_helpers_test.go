package store

import (
	"net/http"
	"testing"
)

func newTestS3Connection(t *testing.T, client *http.Client, endpoint, bucket string) *s3Connection {
	t.Helper()

	conn, err := newS3Connection(s3ConnectionConfig{
		HTTPClient: client,
		Endpoint:   endpoint,
		Bucket:     bucket,
		Region:     "us-east-1",
		PathStyle:  true,
	})
	if err != nil {
		t.Fatalf("new test s3 connection failed: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
	})
	return conn
}

func newTestS3StateStore(t *testing.T, client *http.Client, endpoint, bucket, prefix string) *s3StateStore {
	t.Helper()
	return &s3StateStore{
		MemoryStore: NewMemoryStore(),
		conn:        newTestS3Connection(t, client, endpoint, bucket),
		prefix:      prefix,
	}
}
