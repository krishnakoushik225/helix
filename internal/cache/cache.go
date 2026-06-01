package cache

import (
	"context"
	"time"
)

// Entry holds a cached inference result keyed by a semantic hash of the request.
type Entry struct {
	Key       string    `json:"key"`
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Model     string    `json:"model"`
	HitCount  int       `json:"hit_count"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Stats carries aggregate metrics about the cache.
type Stats struct {
	Hits   int64   `json:"hits"`
	Misses int64   `json:"misses"`
	Keys   int64   `json:"keys"`
	HitRate float64 `json:"hit_rate"`
}

// Cache is the interface every caching backend must satisfy.
type Cache interface {
	// Get retrieves an entry by its semantic key.
	// Returns (nil, nil) on a cache miss.
	Get(ctx context.Context, key string) (*Entry, error)

	// Set stores an entry in the cache with the given TTL.
	Set(ctx context.Context, entry *Entry, ttl time.Duration) error

	// Stats returns current aggregate cache statistics.
	Stats(ctx context.Context) (*Stats, error)
}
