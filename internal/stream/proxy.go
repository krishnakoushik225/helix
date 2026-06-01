package stream

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/krishnakoushik225/helix/internal/providers"
)

// ErrNotFlushable is returned when the ResponseWriter does not support http.Flusher.
var ErrNotFlushable = errors.New("stream: ResponseWriter does not implement http.Flusher")

// Proxy writes SSE events from ch to w until the channel is closed or ctx is cancelled.
//
// Headers are set before the first write so the caller must not have called
// WriteHeader yet. Returns nil on a clean end-of-stream, ctx.Err() on client
// disconnect, or a write error if the connection breaks mid-stream.
func Proxy(ctx context.Context, w http.ResponseWriter, ch <-chan providers.StreamChunk) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return ErrNotFlushable
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				// Channel closed — provider finished cleanly.
				return nil
			}
			if chunk.Done {
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return nil
			}
			if chunk.Delta == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk.Delta); err != nil {
				return fmt.Errorf("stream: write: %w", err)
			}
			flusher.Flush()

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
