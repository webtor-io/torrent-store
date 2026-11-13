package services

import "context"

// RecordObjects is the S3 bucket as private records see it: one object per key,
// no tiers, no caches. *providers.S3 is the implementation; tests use memory.
type RecordObjects interface {
	GetObject(ctx context.Context, key string) ([]byte, error) // ErrNotFound only on 404
	PutObject(ctx context.Context, key string, body []byte) error
	DeleteObject(ctx context.Context, key string) error               // an absent key is not an error
	ListObjects(ctx context.Context, prefix string) ([]string, error) // keys of every page, in key order
}
