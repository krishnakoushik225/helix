package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

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
		r.Post("/v1/chat", chatHandler(registry, database))
		r.Post("/v1/chat/stream", streamChatHandler(registry, database))
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

func chatHandler(registry map[string]providers.Provider, database *db.DB) http.HandlerFunc {
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

		start := time.Now()
		resp, err := p.Complete(r.Context(), &req)
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			log.Error().Err(err).Str("provider", p.Name()).Msg("inference failed")
			writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, resp)

		// Log after response is written so DB latency is invisible to the caller.
		if database != nil {
			costUSD := float64(resp.InputTokens)*p.CostPerInputToken(resp.Model) +
				float64(resp.OutputTokens)*p.CostPerOutputToken(resp.Model)

			logCtx, logCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer logCancel()

			if err := database.LogRequest(logCtx,
				tenantID, p.Name(), resp.Model,
				resp.InputTokens, resp.OutputTokens,
				costUSD, latencyMs, false,
			); err != nil {
				log.Warn().Err(err).Str("provider", p.Name()).Msg("failed to log request")
			}
		}
	}
}

func streamChatHandler(registry map[string]providers.Provider, database *db.DB) http.HandlerFunc {
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

		// Buffer 32 chunks between the provider and the proxy so a slow client
		// doesn't stall the provider's SSE reader.
		ch := make(chan providers.StreamChunk, 32)

		// Forward from providerCh to ch. Stops when providerCh is closed (normal
		// end-of-stream), or when ctx is cancelled (client disconnect or error).
		go func() {
			defer close(ch)
			for {
				select {
				case chunk, ok := <-providerCh:
					if !ok {
						return
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

		if err := stream.Proxy(ctx, w, ch); err != nil {
			log.Warn().Err(err).Str("provider", p.Name()).Msg("stream ended")
		}

		// Log after stream completes. Token counts are unavailable for streaming
		// responses (providers don't return per-chunk usage); log zeros for now.
		if database != nil {
			latencyMs := time.Since(start).Milliseconds()
			model := req.Model
			if model == "" {
				model = p.Name() + "-default"
			}

			logCtx, logCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer logCancel()

			if err := database.LogRequest(logCtx,
				tenantID, p.Name(), model,
				0, 0, 0.0, latencyMs, false,
			); err != nil {
				log.Warn().Err(err).Str("provider", p.Name()).Msg("failed to log stream request")
			}
		}
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
