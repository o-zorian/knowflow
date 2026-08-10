package objectstore

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"knowflow/internal/config"
)

type Client struct {
	client *minio.Client
	bucket string
}

func New(cfg config.MinIO) (*Client, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey.Value(), cfg.SecretKey.Value(), ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create MinIO client: %w", err)
	}
	return &Client{client: client, bucket: cfg.Bucket}, nil
}

func (c *Client) Check(ctx context.Context) error {
	exists, err := c.client.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("check MinIO bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("MinIO bucket is not initialized")
	}
	return nil
}
