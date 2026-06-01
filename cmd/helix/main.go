package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/krishnakoushik225/helix/internal/config"
	"github.com/krishnakoushik225/helix/internal/providers"
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

	ollama := providers.NewOllamaProvider(cfg.OllamaBaseURL)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/health", healthHandler)
	r.Get("/ready", readyHandler)
	r.Post("/v1/chat", chatHandler(ollama))

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

func chatHandler(p providers.Provider) http.HandlerFunc {
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
			writeError(w, http.StatusNotImplemented, "streaming not yet supported on this endpoint")
			return
		}

		resp, err := p.Complete(r.Context(), &req)
		if err != nil {
			log.Error().Err(err).Str("provider", p.Name()).Msg("inference failed")
			writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, resp)
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
