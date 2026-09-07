package storage

import (
	"context"
)

// BlobStorage defines operations for saving raw inbound email payloads.
type BlobStorage interface {
	WriteBytes(ctx context.Context, objectName string, data []byte) error
	Close() error
}

// MemoryStorage implements BlobStorage in-memory for testing.
type MemoryStorage struct {
	Objects map[string][]byte
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{Objects: make(map[string][]byte)}
}

func (m *MemoryStorage) WriteBytes(ctx context.Context, objectName string, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	m.Objects[objectName] = cp
	return nil
}

func (m *MemoryStorage) Close() error {
	return nil
}
