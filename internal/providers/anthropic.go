package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	anthropicBaseURL          = "https://api.anthropic.com/v1"
	anthropicVersion          = "2023-06-01"
	anthropicDefaultModel     = "claude-haiku-4-5-20251001"
	anthropicDefaultMaxTokens = 1024
)

// AnthropicProvider implements Provider against the Anthropic Messages API.
type AnthropicProvider struct {
	apiKey     string
	httpClient *http.Client
}

func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// --- wire types ---

type anthropicReq struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
}

type anthropicResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// splitMessages separates the optional system prompt from the conversation turns.
// Anthropic requires system content in the top-level "system" field, not messages.
func splitMessages(msgs []Message) (system string, turns []Message) {
	for _, m := range msgs {
		if m.Role == "system" {
			system = m.Content
		} else {
			turns = append(turns, m)
		}
	}
	return
}

func (a *AnthropicProvider) Name() string { return "anthropic" }

func (a *AnthropicProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = anthropicDefaultModel
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = anthropicDefaultMaxTokens
	}

	system, turns := splitMessages(req.Messages)
	body, err := json.Marshal(anthropicReq{
		Model:     model,
		Messages:  turns,
		System:    system,
		MaxTokens: maxTok,
		Stream:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicBaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, raw)
	}

	var ar anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	if len(ar.Content) == 0 {
		return nil, fmt.Errorf("anthropic: empty content in response")
	}

	return &Response{
		ID:           ar.ID,
		Model:        ar.Model,
		Content:      ar.Content[0].Text,
		InputTokens:  ar.Usage.InputTokens,
		OutputTokens: ar.Usage.OutputTokens,
		FinishReason: ar.StopReason,
	}, nil
}

func (a *AnthropicProvider) Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error) {
	model := req.Model
	if model == "" {
		model = anthropicDefaultModel
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = anthropicDefaultMaxTokens
	}

	system, turns := splitMessages(req.Messages)
	body, err := json.Marshal(anthropicReq{
		Model:     model,
		Messages:  turns,
		System:    system,
		MaxTokens: maxTok,
		Stream:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicBaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build stream request: %w", err)
	}
	a.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic: stream status %d: %s", resp.StatusCode, raw)
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		defer func() {
			_ = resp.Body.Close()
		}()
		// Anthropic SSE format: paired "event: <type>" / "data: <json>" lines.
		// We track the current event name and act when we see the data line.
		var msgID string
		var currentEvent string

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			switch currentEvent {
			case "message_start":
				// Extract the message ID for use in subsequent chunks.
				var evt struct {
					Message struct {
						ID string `json:"id"`
					} `json:"message"`
				}
				if json.Unmarshal([]byte(data), &evt) == nil {
					msgID = evt.Message.ID
				}

			case "content_block_delta":
				var evt struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if json.Unmarshal([]byte(data), &evt) != nil {
					continue
				}
				if evt.Delta.Type != "text_delta" || evt.Delta.Text == "" {
					continue
				}
				select {
				case ch <- StreamChunk{ID: msgID, Delta: evt.Delta.Text}:
				case <-ctx.Done():
					return
				}

			case "message_stop":
				select {
				case ch <- StreamChunk{ID: msgID, Done: true}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return ch, nil
}

func (a *AnthropicProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicBaseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("anthropic: health check build request: %w", err)
	}
	a.setHeaders(req)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic: health check: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return nil
}

func (a *AnthropicProvider) CostPerInputToken(_ string) float64  { return 0.000003 }
func (a *AnthropicProvider) CostPerOutputToken(_ string) float64 { return 0.000015 }

func (a *AnthropicProvider) setHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("x-api-key", a.apiKey)
	r.Header.Set("anthropic-version", anthropicVersion)
}
