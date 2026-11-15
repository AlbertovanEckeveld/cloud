// Package storage connects to the S3-compatible object store.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

// ErrBucketMissing is returned by Ping when the configured bucket does not exist.
var ErrBucketMissing = errors.New("bucket does not exist")

// Client is the platform's handle on its bucket.
type Client struct {
	s3     *minio.Client
	bucket string
}

// New creates a client. It does not contact the server; use Ping for that.
func New(cfg config.Storage) (*Client, error) {
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	s3, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey.Reveal(), ""),
		Secure:       cfg.UseTLS,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client for %s: %w", cfg.Endpoint, err)
	}
	return &Client{s3: s3, bucket: cfg.Bucket}, nil
}

// Ping verifies that the server is reachable, the credentials are valid and
// the bucket exists.
func (c *Client) Ping(ctx context.Context) error {
	ok, err := c.s3.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", c.bucket, err)
	}
	if !ok {
		return fmt.Errorf("%s: %w", c.bucket, ErrBucketMissing)
	}
	return nil
}

// ErrNotFound is returned by Get for a key that does not exist.
var ErrNotFound = errors.New("object not found")

// Put stores data under key. Keys are never reused for different content
// (new content gets a new key), which keeps replication between sites simple.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := c.s3.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// Get returns the content of key.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := c.s3.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return data, nil
}

// Delete removes key; deleting a missing key is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.s3.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}
