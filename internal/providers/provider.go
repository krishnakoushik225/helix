package providers

import "context"

// Message represents a single turn in a conversation.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// Request is the normalized inference request sent to any provider.
type Request struct {
	Provider    string    `json:"provider,omitempty"` // "ollama" | "anthropic" | "openai"; defaults to "ollama"
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Response is the normalized inference response returned by any provider.
type Response struct {
	ID           string `json:"id"`
	Model        string `json:"model"`
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	FinishReason string `json:"finish_reason"`
}

// StreamChunk is a single delta emitted during a streaming response.
type StreamChunk struct {
	ID    string `json:"id"`
	Delta string `json:"delta"`
	Done  bool   `json:"done"`
}

// Provider is the interface that every LLM backend must satisfy.
type Provider interface {
	// Name returns the unique identifier for this provider (e.g. "anthropic").
	Name() string

	// Complete sends a blocking inference request and returns the full response.
	Complete(ctx context.Context, req *Request) (*Response, error)

	// Stream sends an inference request and writes chunks to the returned channel.
	// The channel is closed when the stream ends or an error occurs.
	Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error)

	// HealthCheck returns nil if the provider backend is reachable.
	HealthCheck(ctx context.Context) error

	// CostPerInputToken returns the provider's cost in USD per input token.
	CostPerInputToken(model string) float64

	// CostPerOutputToken returns the provider's cost in USD per output token.
	CostPerOutputToken(model string) float64
}
