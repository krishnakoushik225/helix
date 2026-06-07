package router

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/krishnakoushik225/helix/internal/fallback"
	"github.com/krishnakoushik225/helix/internal/providers"
)

const windowSize = 100

// latencyTracker maintains a circular buffer of the last 100 observed latencies
// and computes p95 on demand.
type latencyTracker struct {
	mu     sync.Mutex
	window [windowSize]int64
	pos    int // next write position (circular)
	count  int // valid entries, capped at windowSize
}

func (lt *latencyTracker) record(ms int64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.window[lt.pos] = ms
	lt.pos = (lt.pos + 1) % windowSize
	if lt.count < windowSize {
		lt.count++
	}
}

func (lt *latencyTracker) p95() int64 {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	n := lt.count
	if n == 0 {
		return 1000 // default: assume 1 s until we have data
	}
	buf := make([]int64, n)
	// When count < windowSize, valid entries occupy window[0..n-1].
	// When count == windowSize, the circular buffer is full; all 100 entries
	// are valid and we copy them all — order doesn't matter since we sort.
	copy(buf, lt.window[:n])
	sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
	idx := int(math.Ceil(float64(n)*0.95)) - 1
	if idx >= n {
		idx = n - 1
	}
	return buf[idx]
}

// Router selects the best-scoring provider and executes requests with fallback.
type Router struct {
	all      []providers.Provider
	byName   map[string]providers.Provider
	chain    *fallback.Chain
	mu       sync.RWMutex
	trackers map[string]*latencyTracker
}

// New builds a Router from the given provider map and fallback chain.
func New(byName map[string]providers.Provider, chain *fallback.Chain) *Router {
	all := make([]providers.Provider, 0, len(byName))
	trackers := make(map[string]*latencyTracker, len(byName))
	for name, p := range byName {
		all = append(all, p)
		trackers[name] = &latencyTracker{}
	}
	return &Router{
		all:      all,
		byName:   byName,
		chain:    chain,
		trackers: trackers,
	}
}

// score computes a routing score for p (higher = prefer this provider).
//
//	score = (1/p95_ms)×0.4 + (1/cost_per_input_token)×0.4 + availability×0.2
//
// Free providers (cost = 0) are assigned a near-infinite cost score so they
// consistently rank above paid providers, all else being equal.
func (r *Router) score(p providers.Provider) float64 {
	r.mu.RLock()
	tracker := r.trackers[p.Name()]
	r.mu.RUnlock()

	p95 := tracker.p95()
	avail := r.chain.Availability(p.Name())

	cost := p.CostPerInputToken("")
	if cost <= 0 {
		cost = 1e-9 // free → maximise cost-efficiency component
	}

	latencyFactor := 1.0 / math.Max(1.0, float64(p95))
	costFactor := 1.0 / cost

	return latencyFactor*0.4 + costFactor*0.4 + avail*0.2
}

// SortedProviders returns all providers sorted by score (highest first).
// If req.Provider is explicitly set, only that provider is returned.
func (r *Router) SortedProviders(req *providers.Request) []providers.Provider {
	if req.Provider != "" {
		if p, ok := r.byName[req.Provider]; ok {
			return []providers.Provider{p}
		}
		return nil
	}
	all := make([]providers.Provider, len(r.all))
	copy(all, r.all)
	sort.Slice(all, func(i, j int) bool {
		return r.score(all[i]) > r.score(all[j])
	})
	return all
}

// SelectProvider returns the highest-scoring provider whose circuit is not
// strictly open. Used for streaming where fallback after headers are committed
// is not possible.
func (r *Router) SelectProvider(req *providers.Request) (providers.Provider, bool) {
	for _, p := range r.SortedProviders(req) {
		if !r.chain.IsOpen(p.Name()) {
			return p, true
		}
	}
	return nil, false
}

// ProviderByName looks up a provider by its name string.
func (r *Router) ProviderByName(name string) (providers.Provider, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// RecordLatency adds a latency sample for the given provider.
func (r *Router) RecordLatency(name string, ms int64) {
	r.mu.RLock()
	tracker := r.trackers[name]
	r.mu.RUnlock()
	if tracker != nil {
		tracker.record(ms)
	}
}

// normalizeModel returns a shallow clone of req with Model set to "" when the
// caller passed the special "auto" sentinel. Each provider then falls back to
// its own default model rather than receiving the literal string "auto".
// If Model is not "auto" the original pointer is returned unchanged.
func normalizeModel(req *providers.Request) *providers.Request {
	if req.Model != "auto" {
		return req
	}
	clone := *req
	clone.Model = ""
	return &clone
}

// ExecuteWithFallback runs req against providers in score order, falling back
// on failure. Returns 503-style error if all providers fail.
// Sets resp.Provider to the name of the provider that succeeded.
// A Model value of "auto" is stripped before the call so each provider uses
// its own default model.
func (r *Router) ExecuteWithFallback(ctx context.Context, req *providers.Request) (*providers.Response, error) {
	sorted := r.SortedProviders(req)
	if len(sorted) == 0 {
		return nil, fmt.Errorf("unknown provider: %q", req.Provider)
	}

	resp, usedName, ms, err := r.chain.Execute(ctx, normalizeModel(req), sorted)
	if err != nil {
		return nil, err
	}

	resp.Provider = usedName
	r.RecordLatency(usedName, ms)
	return resp, nil
}

// StreamWithFallback opens a streaming channel on the highest-scoring
// non-open provider, trying alternatives if the initial Stream() call fails
// before any bytes are sent.
// Returns the channel, the chosen provider (for cost attribution), and any error.
// A Model value of "auto" is stripped so each provider uses its own default.
func (r *Router) StreamWithFallback(ctx context.Context, req *providers.Request) (<-chan providers.StreamChunk, providers.Provider, error) {
	sorted := r.SortedProviders(req)
	if len(sorted) == 0 {
		return nil, nil, fmt.Errorf("unknown provider: %q", req.Provider)
	}

	effectiveReq := normalizeModel(req)

	for _, p := range sorted {
		if r.chain.IsOpen(p.Name()) {
			continue
		}
		ch, err := p.Stream(ctx, effectiveReq)
		if err != nil {
			r.chain.RecordFailure(p.Name())
			continue
		}
		return ch, p, nil
	}
	return nil, nil, fmt.Errorf("no providers available for streaming")
}
