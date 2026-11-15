//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

func testClient(t *testing.T, mutate func(map[string]string)) *Client {
	t.Helper()
	// Overrides on top of the real environment.
	overrides := map[string]string{}
	if mutate != nil {
		mutate(overrides)
	}
	cfg, err := config.Load(func(k string) string {
		if v, ok := overrides[k]; ok {
			return v
		}
		return os.Getenv(k)
	})
	if err != nil {
		t.Fatalf("integration config: %v", err)
	}
	c, err := New(cfg.Storage)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestPing(t *testing.T) {
	if err := testClient(t, nil).Ping(ctx(t)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingMissingBucket(t *testing.T) {
	c := testClient(t, func(env map[string]string) { env["S3_BUCKET"] = "does-not-exist-" + fmt.Sprint(time.Now().Unix()) })
	err := c.Ping(ctx(t))
	if !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("err = %v, want ErrBucketMissing", err)
	}
}

func TestPingWrongCredentials(t *testing.T) {
	c := testClient(t, func(env map[string]string) { env["S3_SECRET_ACCESS_KEY"] = "wrong-secret" })
	if err := c.Ping(ctx(t)); err == nil {
		t.Fatal("expected error with wrong credentials")
	}
}

// TestRoundTrip proves the credentials can write, read and delete objects,
// which every later module depends on.
func TestRoundTrip(t *testing.T) {
	c := testClient(t, nil)
	key := fmt.Sprintf("_integration-test/%d", time.Now().UnixNano())
	body := []byte("hello from the integration test")

	if _, err := c.s3.PutObject(ctx(t), c.bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	t.Cleanup(func() {
		if err := c.s3.RemoveObject(context.Background(), c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
			t.Errorf("remove: %v", err)
		}
	})

	obj, err := c.s3.GetObject(ctx(t), c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer obj.Close()
	got, err := io.ReadAll(obj)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q, want %q", got, body)
	}
}

func TestPutGetDelete(t *testing.T) {
	c := testClient(t, nil)
	key := fmt.Sprintf("_integration-test/api-%d", time.Now().UnixNano())
	if err := c.Put(ctx(t), key, []byte("inhoud"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx(t), key)
	if err != nil || string(got) != "inhoud" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := c.Delete(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx(t), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
	if err := c.Delete(ctx(t), key); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
}

func TestPutStreamAndOpen(t *testing.T) {
	c := testClient(t, nil)
	key := fmt.Sprintf("_integration-test/stream-%d", time.Now().UnixNano())
	data := bytes.Repeat([]byte("0123456789"), 1000)
	if err := c.PutStream(ctx(t), key, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), key); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	obj, size, err := c.Open(ctx(t), key)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("Open = %d, %v", size, err)
	}
	defer obj.Close()
	// Seeking is what makes HTTP range requests possible.
	if _, err := obj.Seek(9990, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	tail, _ := io.ReadAll(obj)
	if string(tail) != "0123456789" {
		t.Errorf("tail after seek = %q", tail)
	}
	if _, _, err := c.Open(ctx(t), key+"-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing object: err = %v", err)
	}
}
