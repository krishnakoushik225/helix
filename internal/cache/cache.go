package cache

import (
	"context"
	"time"
)

// Entry is a single cached prompt/response pair returned on a cache hit.
type Entry struct {
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Provider  string    `json:"provider"`
	HitCount  int       `json:"hit_count"`
	CreatedAt time.Time `json:"created_at"`
}

// Stats carries aggregate cache metrics derived from the requests table and
// the prompt_cache table.
type Stats struct {
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Keys    int64   `json:"keys"`
	HitRate float64 `json:"hit_rate"`
}

// Cache is the interface for semantic prompt caching.
type Cache interface {
	// Get returns a cached response for the given prompt when a stored
	// embedding is within threshold cosine similarity.
	// Returns (nil, nil) on a cache miss.
	Get(ctx context.Context, prompt string, threshold float64) (*Entry, error)

	// Set generates an embedding for prompt and stores the response.
	Set(ctx context.Context, prompt, response, provider string) error

	// Stats returns aggregate hit/miss counts and total cached entries.
	Stats(ctx context.Context) (*Stats, error)
}
