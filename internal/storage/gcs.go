package storage

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
)

// GCSStorage implements BlobStorage using Google Cloud Storage.
type GCSStorage struct {
	client     *storage.Client
	bucketName string
}

// NewGCSStorage initializes a Google Cloud Storage client.
func NewGCSStorage(ctx context.Context, bucketName string) (*GCSStorage, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	return &GCSStorage{
		client:     client,
		bucketName: bucketName,
	}, nil
}

func (g *GCSStorage) WriteBytes(ctx context.Context, objectName string, data []byte) error {
	bucket := g.client.Bucket(g.bucketName)
	wc := bucket.Object(objectName).NewWriter(ctx)
	wc.ContentType = "message/rfc822"
	wc.Metadata = map[string]string{
		"uploaded_at": time.Now().UTC().Format(time.RFC3339),
	}

	if _, err := wc.Write(data); err != nil {
		_ = wc.Close()
		return fmt.Errorf("failed to write bytes to GCS: %w", err)
	}
	return wc.Close()
}

func (g *GCSStorage) Close() error {
	return g.client.Close()
}
