package fallback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/krishnakoushik225/helix/internal/db"
	"github.com/krishnakoushik225/helix/internal/providers"
)

const (
	failureThreshold = 5                // failures inside failureWindow to open the circuit
	failureWindow    = 60 * time.Second // sliding window for counting failures
	cooldownPeriod   = 30 * time.Second // open → half_open transition delay
)

type circuitState int

const (
	stateClosed circuitState = iota
	stateOpen
	stateHalfOpen
)

func (s circuitState) String() string {
	switch s {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

func parseState(s string) circuitState {
	switch s {
	case "open":
		return stateOpen
	case "half_open":
		return stateHalfOpen
	default:
		return stateClosed
	}
}

type circuitBreaker struct {
	state    circuitState
	failures []time.Time // timestamps of recent failures within failureWindow
	openedAt time.Time
}

// Chain holds per-provider circuit breakers and executes requests with fallback.
type Chain struct {
	mu       sync.Mutex
	breakers map[string]*circuitBreaker
	database *db.DB // optional; nil means no persistence
}

// New creates a Chain. If database is non-nil, circuit state is loaded from
// the provider_health table on startup and persisted asynchronously on change.
func New(database *db.DB) *Chain {
	c := &Chain{
		breakers: make(map[string]*circuitBreaker),
		database: database,
	}
	if database != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.loadState(ctx)
	}
	return c
}

func (c *Chain) getOrCreate(name string) *circuitBreaker {
	// caller must hold c.mu
	cb, ok := c.breakers[name]
	if !ok {
		cb = &circuitBreaker{state: stateClosed}
		c.breakers[name] = cb
	}
	return cb
}

// maybeTransitionLocked moves an open circuit to half_open once cooldown passes.
// Must be called with c.mu held.
func (c *Chain) maybeTransitionLocked(cb *circuitBreaker, name string) {
	if cb.state == stateOpen && time.Since(cb.openedAt) >= cooldownPeriod {
		cb.state = stateHalfOpen
		log.Info().Str("provider", name).Msg("circuit half-open (testing)")
	}
}

// Availability returns the scoring availability component for a provider:
//
//	1.0 = closed  (fully healthy)
//	0.5 = half_open (degraded, one probe allowed)
//	0.0 = open    (skip in scoring)
func (c *Chain) Availability(name string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	cb := c.getOrCreate(name)
	c.maybeTransitionLocked(cb, name)
	switch cb.state {
	case stateClosed:
		return 1.0
	case stateHalfOpen:
		return 0.5
	default:
		return 0.0
	}
}

// IsOpen returns true only when the circuit is strictly open (not half_open).
// Use this to hard-skip a provider for stream selection.
func (c *Chain) IsOpen(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cb := c.getOrCreate(name)
	c.maybeTransitionLocked(cb, name)
	return cb.state == stateOpen
}

// RecordSuccess closes the circuit when it was half_open.
func (c *Chain) RecordSuccess(name string) {
	c.mu.Lock()
	cb := c.getOrCreate(name)
	if cb.state == stateHalfOpen {
		cb.state = stateClosed
		cb.failures = nil
		log.Info().Str("provider", name).Msg("circuit closed (recovered)")
		c.mu.Unlock()
		c.persistState(name)
		return
	}
	c.mu.Unlock()
}

// RecordFailure appends a failure timestamp, prunes the sliding window, and
// opens the circuit when the threshold is reached.
func (c *Chain) RecordFailure(name string) {
	now := time.Now()
	c.mu.Lock()
	cb := c.getOrCreate(name)

	// Prune failures outside the window.
	cutoff := now.Add(-failureWindow)
	i := 0
	for i < len(cb.failures) && cb.failures[i].Before(cutoff) {
		i++
	}
	cb.failures = append(cb.failures[i:], now)

	if (cb.state == stateClosed || cb.state == stateHalfOpen) &&
		len(cb.failures) >= failureThreshold {
		cb.state = stateOpen
		cb.openedAt = now
		log.Warn().Str("provider", name).Int("failures", len(cb.failures)).
			Msg("circuit opened")
	}
	c.mu.Unlock()
	c.persistState(name)
}

// Execute tries each provider in order, skipping open circuits, and records
// outcomes. Returns the response, the winning provider name, latency in ms,
// and an error wrapping the last failure (or a generic "no providers
// available" error when all were skipped by open circuits).
func (c *Chain) Execute(
	ctx context.Context,
	req *providers.Request,
	ordered []providers.Provider,
) (*providers.Response, string, int64, error) {
	var lastErr error
	attempted := 0

	for _, p := range ordered {
		name := p.Name()

		// Check circuit and allow half_open probes through.
		c.mu.Lock()
		cb := c.getOrCreate(name)
		c.maybeTransitionLocked(cb, name)
		allowed := cb.state != stateOpen
		c.mu.Unlock()

		if !allowed {
			continue
		}
		attempted++

		start := time.Now()
		resp, err := p.Complete(ctx, req)
		ms := time.Since(start).Milliseconds()

		if err != nil {
			lastErr = err
			c.RecordFailure(name)
			log.Warn().Err(err).Str("provider", name).Int64("latency_ms", ms).
				Msg("provider failed, trying next")
			continue
		}

		c.RecordSuccess(name)
		return resp, name, ms, nil
	}

	if attempted == 0 {
		return nil, "", 0, errors.New("no providers available: all circuits open")
	}
	return nil, "", 0, fmt.Errorf("all providers failed: %w", lastErr)
}

// --- DB persistence ---

func (c *Chain) loadState(ctx context.Context) {
	rows, err := c.database.Pool().Query(ctx,
		`SELECT provider, state, opened_at FROM provider_health`)
	if err != nil {
		log.Warn().Err(err).Msg("fallback: could not load provider_health (table may not exist)")
		return
	}
	defer rows.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var name, stateStr string
		var openedAt *time.Time
		if err := rows.Scan(&name, &stateStr, &openedAt); err != nil {
			continue
		}
		cb := c.getOrCreate(name)
		cb.state = parseState(stateStr)
		if openedAt != nil {
			cb.openedAt = *openedAt
		}
	}
}

func (c *Chain) persistState(name string) {
	if c.database == nil {
		return
	}

	// Snapshot under lock so the goroutine uses consistent values.
	c.mu.Lock()
	cb := c.getOrCreate(name)
	state := cb.state.String()
	openedAt := cb.openedAt
	failCount := len(cb.failures)
	var lastFailAt *time.Time
	if failCount > 0 {
		t := cb.failures[len(cb.failures)-1]
		lastFailAt = &t
	}
	c.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var openedAtArg *time.Time
		if !openedAt.IsZero() {
			openedAtArg = &openedAt
		}

		_, err := c.database.Pool().Exec(ctx, `
			INSERT INTO provider_health
				(provider, state, failure_count, last_failure_at, opened_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (provider) DO UPDATE SET
				state           = EXCLUDED.state,
				failure_count   = EXCLUDED.failure_count,
				last_failure_at = EXCLUDED.last_failure_at,
				opened_at       = EXCLUDED.opened_at,
				updated_at      = NOW()
		`, name, state, failCount, lastFailAt, openedAtArg)
		if err != nil {
			log.Warn().Err(err).Str("provider", name).
				Msg("fallback: failed to persist circuit state")
		}
	}()
}
