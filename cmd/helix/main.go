package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/krishnakoushik225/helix/internal/cache"
	"github.com/krishnakoushik225/helix/internal/config"
	"github.com/krishnakoushik225/helix/internal/db"
	"github.com/krishnakoushik225/helix/internal/fallback"
	authmw "github.com/krishnakoushik225/helix/internal/middleware"
	obs "github.com/krishnakoushik225/helix/internal/observability"
	"github.com/krishnakoushik225/helix/internal/providers"
	"github.com/krishnakoushik225/helix/internal/router"
	"github.com/krishnakoushik225/helix/internal/stream"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	if cfg.Env == "production" {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	log.Info().
		Str("env", cfg.Env).
		Str("port", cfg.Port).
		Msg("starting helix")

	// DB is optional — skip if DATABASE_URL is not configured.
	var database *db.DB
	if cfg.DatabaseURL != "" {
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dbCancel()
		database, err = db.Connect(dbCtx, cfg.DatabaseURL)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to connect to database")
		}
		defer database.Close()
		log.Info().Msg("database connected")
	} else {
		log.Warn().Msg("DATABASE_URL not set — request logging disabled")
	}

	// Semantic cache is optional — requires CACHE_ENABLED=true, a DB connection,
	// and a valid OPENAI_API_KEY for embedding generation.
	var semanticCache cache.Cache
	if cfg.CacheEnabled && database != nil && cfg.OpenAIAPIKey != "" {
		semanticCache = cache.NewSemanticCache(database.Pool(), cfg.OpenAIAPIKey)
		log.Info().
			Float64("threshold", cfg.CacheSimilarityThreshold).
			Msg("semantic cache enabled")
	} else {
		log.Warn().Msg("semantic cache disabled")
	}

	// Rate limiter is optional — skipped if REDIS_URL is not configured.
	var rateLimiter *authmw.RateLimiter
	if cfg.RedisURL != "" {
		rateLimiter, err = authmw.NewRateLimiter(cfg.RedisURL, cfg.RateLimitRPM)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to connect to Redis")
		}
		defer func() {
			if err := rateLimiter.Close(); err != nil {
				log.Warn().Err(err).Msg("failed to close rate limiter")
			}
		}()
		log.Info().Int("rpm", cfg.RateLimitRPM).Msg("rate limiter connected")
	} else {
		log.Warn().Msg("REDIS_URL not set — rate limiting disabled")
	}

	// Build provider registry conditionally — only register providers that are
	// actually configured so the router doesn't score or attempt unconfigured ones.
	providerMap := map[string]providers.Provider{
		"ollama": providers.NewOllamaProvider(cfg.OllamaBaseURL),
	}
	log.Info().Msg("provider registered: ollama")

	if cfg.OpenAIAPIKey != "" {
		providerMap["openai"] = providers.NewOpenAIProvider(cfg.OpenAIAPIKey)
		log.Info().Msg("provider registered: openai")
	} else {
		log.Warn().Msg("OPENAI_API_KEY not set — openai provider disabled")
	}

	if cfg.AnthropicAPIKey != "" {
		providerMap["anthropic"] = providers.NewAnthropicProvider(cfg.AnthropicAPIKey)
		log.Info().Msg("provider registered: anthropic")
	} else {
		log.Warn().Msg("ANTHROPIC_API_KEY not set — anthropic provider disabled")
	}

	fbChain := fallback.New(database)
	rtr := router.New(providerMap, fbChain)
	metrics := obs.New()

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)

	r.Get("/health", healthHandler)
	r.Get("/ready", readyHandler)
	r.Get("/metrics", promhttp.Handler().ServeHTTP) // public — scraped by Prometheus

	// Protected routes — JWT required, then rate-limited per tenant.
	r.Group(func(r chi.Router) {
		r.Use(authmw.Require(cfg.JWTSecret))
		if rateLimiter != nil {
			r.Use(rateLimiter.Middleware())
		}
		r.Post("/v1/chat", chatHandler(rtr, database, semanticCache, cfg.CacheSimilarityThreshold, metrics))
		r.Post("/v1/chat/stream", streamChatHandler(rtr, database, semanticCache, cfg.CacheSimilarityThreshold, metrics))
		r.Get("/v1/stats", statsHandler(database))
	})

	srv := &http.Server{
		Addr:        ":" + cfg.Port,
		Handler:     r,
		ReadTimeout: 10 * time.Second,
		// WriteTimeout must be generous enough for LLM completions.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		log.Info().Str("addr", srv.Addr).Msg("server listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	<-ctx.Done()
	log.Info().Msg("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown failed")
	}

	log.Info().Msg("stopped")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func chatHandler(
	rtr *router.Router,
	database *db.DB,
	semanticCache cache.Cache,
	cacheThreshold float64,
	metrics *obs.Metrics,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Deferred recorder fires on every exit path.
		var (
			providerName = "unknown"
			modelName    = "unknown"
			statusStr    = "400"
			cacheHitStr  = "false"
		)
		defer func() {
			metrics.RequestsTotal.WithLabelValues(providerName, modelName, statusStr, cacheHitStr).Inc()
			metrics.RequestDuration.WithLabelValues(providerName, modelName).Observe(time.Since(start).Seconds())
		}()

		var req providers.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if len(req.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "messages must not be empty")
			return
		}
		if req.Stream {
			statusStr = "501"
			writeError(w, http.StatusNotImplemented, "use /v1/chat/stream for streaming")
			return
		}

		tenantID := authmw.GetTenantID(r.Context())
		prompt := buildPrompt(req.Messages)

		// --- cache lookup ---
		if semanticCache != nil {
			hit, err := semanticCache.Get(r.Context(), prompt, cacheThreshold)
			if err != nil {
				log.Warn().Err(err).Msg("cache lookup failed")
			} else if hit != nil {
				providerName = hit.Provider
				modelName = "cached"
				statusStr = "200"
				cacheHitStr = "true"
				metrics.CacheHitsTotal.Inc()
				w.Header().Set("X-Cache-Hit", "true")
				writeJSON(w, http.StatusOK, &providers.Response{
					Provider: hit.Provider,
					Content:  hit.Response,
				})
				logUsage(database, tenantID, hit.Provider, hit.Provider, 0, 0, 0, 0, true)
				return
			}
		}

		// --- router: execute with automatic fallback ---
		resp, err := rtr.ExecuteWithFallback(r.Context(), &req)
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			statusStr = "503"
			log.Error().Err(err).Msg("all providers failed")
			writeError(w, http.StatusServiceUnavailable, "inference failed: "+err.Error())
			return
		}

		providerName = resp.Provider
		modelName = resp.Model
		statusStr = "200"

		writeJSON(w, http.StatusOK, resp)

		// Async: populate cache so it doesn't add latency.
		if semanticCache != nil {
			pName := resp.Provider
			go func() {
				cacheCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := semanticCache.Set(cacheCtx, prompt, resp.Content, pName); err != nil {
					log.Warn().Err(err).Msg("cache set failed")
				}
			}()
		}

		var costUSD float64
		if p, ok := rtr.ProviderByName(resp.Provider); ok {
			costUSD = float64(resp.InputTokens)*p.CostPerInputToken(resp.Model) +
				float64(resp.OutputTokens)*p.CostPerOutputToken(resp.Model)
		}

		// Token and cost counters (provider calls only, not cache hits).
		metrics.TokensTotal.WithLabelValues(tenantID, resp.Provider, "input").Add(float64(resp.InputTokens))
		metrics.TokensTotal.WithLabelValues(tenantID, resp.Provider, "output").Add(float64(resp.OutputTokens))
		metrics.CostUSDTotal.WithLabelValues(tenantID, resp.Provider).Add(costUSD)

		logUsage(database, tenantID, resp.Provider, resp.Model,
			resp.InputTokens, resp.OutputTokens, costUSD, latencyMs, false)
	}
}

func streamChatHandler(
	rtr *router.Router,
	database *db.DB,
	semanticCache cache.Cache,
	cacheThreshold float64,
	metrics *obs.Metrics,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tenantID := authmw.GetTenantID(r.Context())

		// Deferred recorder fires on every exit path.
		var (
			providerName = "unknown"
			modelName    = "unknown"
			statusStr    = "400"
			cacheHitStr  = "false"
		)
		defer func() {
			metrics.RequestsTotal.WithLabelValues(providerName, modelName, statusStr, cacheHitStr).Inc()
			metrics.RequestDuration.WithLabelValues(providerName, modelName).Observe(time.Since(start).Seconds())
		}()

		var req providers.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if len(req.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "messages must not be empty")
			return
		}

		prompt := buildPrompt(req.Messages)

		// --- cache lookup: synthesise SSE from cached text on hit ---
		if semanticCache != nil {
			hit, err := semanticCache.Get(r.Context(), prompt, cacheThreshold)
			if err != nil {
				log.Warn().Err(err).Msg("cache lookup failed")
			} else if hit != nil {
				providerName = hit.Provider
				modelName = "cached"
				statusStr = "200"
				cacheHitStr = "true"
				metrics.CacheHitsTotal.Inc()
				w.Header().Set("X-Cache-Hit", "true")
				synth := make(chan providers.StreamChunk, 2)
				synth <- providers.StreamChunk{Delta: hit.Response}
				synth <- providers.StreamChunk{Done: true}
				close(synth)
				_ = stream.Proxy(r.Context(), w, synth)
				logUsage(database, tenantID, hit.Provider, hit.Provider, 0, 0, 0, time.Since(start).Milliseconds(), true)
				return
			}
		}

		// Child context: cancel() is called on handler return, which stops the
		// forwarding goroutine and the provider goroutine regardless of why we exit.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// StreamWithFallback tries providers in score order until one starts streaming.
		providerCh, p, err := rtr.StreamWithFallback(ctx, &req)
		if err != nil {
			statusStr = "503"
			log.Error().Err(err).Msg("all stream providers failed")
			writeError(w, http.StatusServiceUnavailable, "streaming failed: "+err.Error())
			return
		}

		// Track open streams from this point — the response is committed.
		metrics.ActiveStreams.Inc()
		defer metrics.ActiveStreams.Dec()

		providerName = p.Name()
		modelName = req.Model
		if modelName == "" || modelName == "auto" {
			modelName = p.Name() + "-default"
		}
		statusStr = "200"

		// Buffer 32 chunks. The forwarding goroutine also accumulates deltas so we
		// can populate the cache after a successful stream.
		ch := make(chan providers.StreamChunk, 32)
		var accum strings.Builder

		go func() {
			defer close(ch)
			for {
				select {
				case chunk, ok := <-providerCh:
					if !ok {
						return
					}
					if !chunk.Done && chunk.Delta != "" {
						accum.WriteString(chunk.Delta)
					}
					select {
					case ch <- chunk:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		streamErr := stream.Proxy(ctx, w, ch)
		if streamErr != nil {
			statusStr = "499" // client closed connection
			log.Warn().Err(streamErr).Str("provider", p.Name()).Msg("stream ended")
		}

		latencyMs := time.Since(start).Milliseconds()

		// Cache and log only after a clean, complete stream.
		// When streamErr != nil the goroutine may still be running briefly —
		// reading accum would race. When nil the goroutine has already closed ch.
		if streamErr == nil {
			if semanticCache != nil {
				if full := accum.String(); full != "" {
					pName := p.Name()
					go func() {
						cacheCtx, cacheCancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cacheCancel()
						if err := semanticCache.Set(cacheCtx, prompt, full, pName); err != nil {
							log.Warn().Err(err).Msg("cache set failed")
						}
					}()
				}
			}
			rtr.RecordLatency(p.Name(), latencyMs)
		}

		logUsage(database, tenantID, p.Name(), modelName, 0, 0, 0, latencyMs, false)
	}
}

func statsHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if database == nil {
			writeError(w, http.StatusServiceUnavailable, "database not configured")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		result, err := database.Stats(ctx)
		if err != nil {
			log.Error().Err(err).Msg("stats query failed")
			writeError(w, http.StatusInternalServerError, "failed to query stats")
			return
		}

		var cacheHitRate float64
		if result.TotalRequests > 0 {
			cacheHitRate = float64(result.CacheHits) / float64(result.TotalRequests) * 100.0
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"total_requests":     result.TotalRequests,
			"cache_hit_rate":     cacheHitRate,
			"avg_latency_ms":     result.AvgLatencyMs,
			"cost_today_usd":     result.CostTodayUSD,
			"provider_breakdown": result.ProviderBreakdown,
		})
	}
}

// buildPrompt serialises the message list to a canonical JSON string used as
// the cache key. Encoding the whole array captures conversation context so
// identical questions with different history don't collide.
func buildPrompt(messages []providers.Message) string {
	b, _ := json.Marshal(messages)
	return string(b)
}

// logUsage writes one row to the requests table, logging errors as warnings.
// It is always called after the response is written so DB latency is invisible
// to the caller. A fresh 5s context insulates it from the request context.
func logUsage(
	database *db.DB,
	tenantID, provider, model string,
	inputTokens, outputTokens int,
	costUSD float64,
	latencyMs int64,
	cacheHit bool,
) {
	if database == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.LogRequest(ctx,
		tenantID, provider, model,
		inputTokens, outputTokens, costUSD, latencyMs, cacheHit,
	); err != nil {
		log.Warn().Err(err).Str("provider", provider).Msg("failed to log request")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
