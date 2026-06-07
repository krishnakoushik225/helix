package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxConns = 10

// DB wraps a pgx connection pool scoped to the Supabase (Postgres) database.
type DB struct {
	pool *pgxpool.Pool
}

// Tenant is a row from the tenants table.
type Tenant struct {
	ID     string
	APIKey string
}

// Connect opens a connection pool to databaseURL, pings it, and returns a DB.
func Connect(ctx context.Context, databaseURL string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases all connections in the pool.
func (db *DB) Close() { db.pool.Close() }

// Pool exposes the underlying pgxpool for packages that need direct query access.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// LogRequest inserts one row into the requests table.
// tenantID may be empty when no auth context is available (inserts NULL).
// promptTokens and completionTokens may be 0 for streaming responses where
// per-chunk usage data is not returned by the provider.
func (db *DB) LogRequest(
	ctx context.Context,
	tenantID, provider, model string,
	promptTokens, completionTokens int,
	costUSD float64,
	latencyMs int64,
	cacheHit bool,
) error {
	// Convert empty tenantID → NULL to avoid FK violations before auth is wired in.
	var tid *string
	if tenantID != "" {
		tid = &tenantID
	}

	_, err := db.pool.Exec(ctx,
		`INSERT INTO requests
			(tenant_id, provider, model, prompt_tokens, completion_tokens, cost_usd, latency_ms, cache_hit)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tid, provider, model, promptTokens, completionTokens, costUSD, latencyMs, cacheHit,
	)
	if err != nil {
		return fmt.Errorf("db: log request: %w", err)
	}
	return nil
}

// ProviderBreakdown aggregates per-provider metrics for the last 24 hours.
type ProviderBreakdown struct {
	Requests   int64   `json:"requests"`
	CostUSD    float64 `json:"cost_usd"`
	AvgLatency float64 `json:"avg_latency_ms"`
}

// StatsResult is returned by Stats and drives the /v1/stats endpoint.
type StatsResult struct {
	TotalRequests     int64                        `json:"total_requests"`
	CacheHits         int64                        `json:"cache_hits"`
	AvgLatencyMs      float64                      `json:"avg_latency_ms"`
	CostTodayUSD      float64                      `json:"cost_today_usd"`
	ProviderBreakdown map[string]ProviderBreakdown `json:"provider_breakdown"`
}

// Stats queries aggregate request statistics from the requests table.
func (db *DB) Stats(ctx context.Context) (*StatsResult, error) {
	result := &StatsResult{
		ProviderBreakdown: make(map[string]ProviderBreakdown),
	}

	if err := db.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE cache_hit = true),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(cost_usd) FILTER (WHERE created_at >= CURRENT_DATE), 0)
		FROM requests
	`).Scan(
		&result.TotalRequests,
		&result.CacheHits,
		&result.AvgLatencyMs,
		&result.CostTodayUSD,
	); err != nil {
		return nil, fmt.Errorf("db: stats summary: %w", err)
	}

	rows, err := db.pool.Query(ctx, `
		SELECT
			provider,
			COUNT(*),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM requests
		WHERE created_at >= NOW() - INTERVAL '24 hours'
		GROUP BY provider
	`)
	if err != nil {
		return nil, fmt.Errorf("db: stats breakdown: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var pb ProviderBreakdown
		if err := rows.Scan(&name, &pb.Requests, &pb.CostUSD, &pb.AvgLatency); err != nil {
			continue
		}
		result.ProviderBreakdown[name] = pb
	}
	return result, nil
}

// GetTenant returns the tenant whose api_key matches the argument.
// Returns an error wrapping pgx.ErrNoRows if no tenant is found.
func (db *DB) GetTenant(ctx context.Context, apiKey string) (*Tenant, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, api_key FROM tenants WHERE api_key = $1`, apiKey)

	var t Tenant
	if err := row.Scan(&t.ID, &t.APIKey); err != nil {
		return nil, fmt.Errorf("db: get tenant: %w", err)
	}
	return &t, nil
}
