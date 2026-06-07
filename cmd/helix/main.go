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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/krishnakoushik225/helix/internal/cache"
	"github.com/krishnakoushik225/helix/internal/config"
	"github.com/krishnakoushik225/helix/internal/db"
	authmw "github.com/krishnakoushik225/helix/internal/middleware"
	"github.com/krishnakoushik225/helix/internal/providers"
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
		defer rateLimiter.Close()
		log.Info().Int("rpm", cfg.RateLimitRPM).Msg("rate limiter connected")
	} else {
		log.Warn().Msg("REDIS_URL not set — rate limiting disabled")
	}

	registry := map[string]providers.Provider{
		"ollama":    providers.NewOllamaProvider(cfg.OllamaBaseURL),
		"anthropic": providers.NewAnthropicProvider(cfg.AnthropicAPIKey),
		"openai":    providers.NewOpenAIProvider(cfg.OpenAIAPIKey),
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)

	r.Get("/health", healthHandler)
	r.Get("/ready", readyHandler)

	// Protected routes — JWT required, then rate-limited per tenant.
	r.Group(func(r chi.Router) {
		r.Use(authmw.Require(cfg.JWTSecret))
		if rateLimiter != nil {
			r.Use(rateLimiter.Middleware())
		}
		r.Post("/v1/chat", chatHandler(registry, database, semanticCache, cfg.CacheSimilarityThreshold))
		r.Post("/v1/chat/stream", streamChatHandler(registry, database, semanticCache, cfg.CacheSimilarityThreshold))
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
	registry map[string]providers.Provider,
	database *db.DB,
	semanticCache cache.Cache,
	cacheThreshold float64,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			writeError(w, http.StatusNotImplemented, "use /v1/chat/stream for streaming")
			return
		}

		tenantID := authmw.GetTenantID(r.Context())

		p, ok := resolveProvider(registry, req.Provider)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown provider: "+req.Provider)
			return
		}

		prompt := buildPrompt(req.Messages)

		// --- cache lookup ---
		if semanticCache != nil {
			hit, err := semanticCache.Get(r.Context(), prompt, cacheThreshold)
			if err != nil {
				log.Warn().Err(err).Msg("cache lookup failed")
			} else if hit != nil {
				w.Header().Set("X-Cache-Hit", "true")
				writeJSON(w, http.StatusOK, &providers.Response{
					Model:   hit.Provider,
					Content: hit.Response,
				})
				logUsage(database, tenantID, p.Name(), p.Name(), 0, 0, 0, 0, true)
				return
			}
		}

		// --- provider call ---
		start := time.Now()
		resp, err := p.Complete(r.Context(), &req)
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			log.Error().Err(err).Str("provider", p.Name()).Msg("inference failed")
			writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, resp)

		// Async: populate cache so it doesn't add latency.
		if semanticCache != nil {
			go func() {
				cacheCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := semanticCache.Set(cacheCtx, prompt, resp.Content, p.Name()); err != nil {
					log.Warn().Err(err).Msg("cache set failed")
				}
			}()
		}

		costUSD := float64(resp.InputTokens)*p.CostPerInputToken(resp.Model) +
			float64(resp.OutputTokens)*p.CostPerOutputToken(resp.Model)
		logUsage(database, tenantID, p.Name(), resp.Model,
			resp.InputTokens, resp.OutputTokens, costUSD, latencyMs, false)
	}
}

func streamChatHandler(
	registry map[string]providers.Provider,
	database *db.DB,
	semanticCache cache.Cache,
	cacheThreshold float64,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tenantID := authmw.GetTenantID(r.Context())

		var req providers.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if len(req.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "messages must not be empty")
			return
		}

		p, ok := resolveProvider(registry, req.Provider)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown provider: "+req.Provider)
			return
		}

		prompt := buildPrompt(req.Messages)

		// --- cache lookup: synthesise SSE from cached text on hit ---
		if semanticCache != nil {
			hit, err := semanticCache.Get(r.Context(), prompt, cacheThreshold)
			if err != nil {
				log.Warn().Err(err).Msg("cache lookup failed")
			} else if hit != nil {
				w.Header().Set("X-Cache-Hit", "true")
				synth := make(chan providers.StreamChunk, 2)
				synth <- providers.StreamChunk{Delta: hit.Response}
				synth <- providers.StreamChunk{Done: true}
				close(synth)
				_ = stream.Proxy(r.Context(), w, synth)
				logUsage(database, tenantID, p.Name(), p.Name(), 0, 0, 0, time.Since(start).Milliseconds(), true)
				return
			}
		}

		// Child context: cancel() is called on handler return, which stops the
		// forwarding goroutine and the provider goroutine regardless of why we exit.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Call Stream synchronously so a connection error returns a clean JSON 502
		// before we commit to SSE headers.
		providerCh, err := p.Stream(ctx, &req)
		if err != nil {
			log.Error().Err(err).Str("provider", p.Name()).Msg("stream init failed")
			writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
			return
		}

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
			log.Warn().Err(streamErr).Str("provider", p.Name()).Msg("stream ended")
		}

		latencyMs := time.Since(start).Milliseconds()

		// Cache and log only after a clean, complete stream.
		// When streamErr != nil the goroutine may still be running briefly —
		// reading accum would race. When nil the goroutine has already closed ch.
		if streamErr == nil {
			if semanticCache != nil {
				if full := accum.String(); full != "" {
					go func() {
						cacheCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := semanticCache.Set(cacheCtx, prompt, full, p.Name()); err != nil {
							log.Warn().Err(err).Msg("cache set failed")
						}
					}()
				}
			}
		}

		model := req.Model
		if model == "" {
			model = p.Name() + "-default"
		}
		logUsage(database, tenantID, p.Name(), model, 0, 0, 0, latencyMs, false)
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

// resolveProvider looks up a provider by name, defaulting to "ollama".
func resolveProvider(registry map[string]providers.Provider, name string) (providers.Provider, bool) {
	if name == "" {
		name = "ollama"
	}
	p, ok := registry[name]
	return p, ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
