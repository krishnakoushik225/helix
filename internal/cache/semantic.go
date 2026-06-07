package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

const (
	embeddingModel = "text-embedding-3-small"
	embeddingURL   = "https://api.openai.com/v1/embeddings"
)

// SemanticCache implements Cache using pgvector cosine-similarity lookups
// and OpenAI text-embedding-3-small for vector generation.
type SemanticCache struct {
	pool       *pgxpool.Pool
	httpClient *http.Client
	apiKey     string
}

// NewSemanticCache returns a SemanticCache that stores vectors in the
// prompt_cache table reachable via pool and generates embeddings with apiKey.
func NewSemanticCache(pool *pgxpool.Pool, apiKey string) *SemanticCache {
	return &SemanticCache{
		pool:       pool,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiKey:     apiKey,
	}
}

// Get embeds prompt and returns the most similar cached entry whose
// cosine similarity exceeds threshold. Returns (nil, nil) on a miss.
func (c *SemanticCache) Get(ctx context.Context, prompt string, threshold float64) (*Entry, error) {
	vec, err := c.embed(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("cache: get: embed: %w", err)
	}

	vecStr := formatVector(vec)

	var entry Entry
	var promptHash string
	var similarity float64

	err = c.pool.QueryRow(ctx, `
		SELECT prompt_hash, prompt, response, provider, hit_count, created_at,
		       1-(embedding <=> $1::vector) AS similarity
		FROM prompt_cache
		WHERE 1-(embedding <=> $1::vector) > $2
		ORDER BY similarity DESC
		LIMIT 1
	`, vecStr, threshold).Scan(
		&promptHash, &entry.Prompt, &entry.Response, &entry.Provider,
		&entry.HitCount, &entry.CreatedAt, &similarity,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("cache: get: query: %w", err)
	}

	// Increment hit counter asynchronously — don't add latency to the response.
	go func() {
		_, dbErr := c.pool.Exec(context.Background(),
			`UPDATE prompt_cache SET hit_count = hit_count + 1 WHERE prompt_hash = $1`,
			promptHash,
		)
		if dbErr != nil {
			log.Warn().Err(dbErr).Msg("cache: failed to increment hit_count")
		}
	}()

	return &entry, nil
}

// Set embeds prompt and inserts a new cache entry.
// Duplicate prompts (same SHA-256 hash) are silently ignored.
func (c *SemanticCache) Set(ctx context.Context, prompt, response, provider string) error {
	vec, err := c.embed(ctx, prompt)
	if err != nil {
		return fmt.Errorf("cache: set: embed: %w", err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))
	vecStr := formatVector(vec)

	// Use WHERE NOT EXISTS to deduplicate without requiring a DB-level UNIQUE
	// constraint on prompt_hash (the table may not have one yet).
	_, err = c.pool.Exec(ctx, `
		INSERT INTO prompt_cache (prompt, prompt_hash, response, provider, embedding)
		SELECT $1, $2, $3, $4, $5::vector
		WHERE NOT EXISTS (
			SELECT 1 FROM prompt_cache WHERE prompt_hash = $2
		)
	`, prompt, hash, response, provider, vecStr)
	if err != nil {
		return fmt.Errorf("cache: set: insert: %w", err)
	}
	return nil
}

// Stats queries both the requests table (hits/misses) and prompt_cache (key count).
func (c *SemanticCache) Stats(ctx context.Context) (*Stats, error) {
	var s Stats

	err := c.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE cache_hit = true),
			COUNT(*) FILTER (WHERE cache_hit = false)
		FROM requests
	`).Scan(&s.Hits, &s.Misses)
	if err != nil {
		return nil, fmt.Errorf("cache: stats: requests: %w", err)
	}

	if err := c.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM prompt_cache`,
	).Scan(&s.Keys); err != nil {
		return nil, fmt.Errorf("cache: stats: keys: %w", err)
	}

	total := s.Hits + s.Misses
	if total > 0 {
		s.HitRate = float64(s.Hits) / float64(total)
	}
	return &s, nil
}

// --- embedding ---

type embedReq struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (c *SemanticCache) embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedReq{Model: embeddingModel, Input: text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embeddingURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings API %d: %s", resp.StatusCode, raw)
	}

	var er embedResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("embeddings decode: %w", err)
	}
	if len(er.Data) == 0 || len(er.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings: empty response")
	}
	return er.Data[0].Embedding, nil
}

// formatVector encodes a float32 slice as pgvector's text format: [v1,v2,...,vn]
// Uses 'f' format (never scientific notation) so pgvector parses it correctly.
func formatVector(v []float32) string {
	sb := strings.Builder{}
	sb.Grow(len(v)*12 + 2)
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}
