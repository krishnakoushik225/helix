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

const ollamaDefaultModel = "llama3"

// OllamaProvider implements Provider against Ollama's OpenAI-compatible API.
type OllamaProvider struct {
	baseURL    string
	httpClient *http.Client
}

func NewOllamaProvider(baseURL string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{},
	}
}

// --- OpenAI-compatible wire types ---

type ollamaReq struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream"`
}

type ollamaResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type ollamaChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (o *OllamaProvider) Name() string { return "ollama" }

func (o *OllamaProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = ollamaDefaultModel
	}

	body, err := json.Marshal(ollamaReq{
		Model:       model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      false,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, raw)
	}

	var or ollamaResp
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if len(or.Choices) == 0 {
		return nil, fmt.Errorf("ollama: empty choices in response")
	}

	return &Response{
		ID:           or.ID,
		Model:        or.Model,
		Content:      or.Choices[0].Message.Content,
		InputTokens:  or.Usage.PromptTokens,
		OutputTokens: or.Usage.CompletionTokens,
		FinishReason: or.Choices[0].FinishReason,
	}, nil
}

func (o *OllamaProvider) Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error) {
	model := req.Model
	if model == "" {
		model = ollamaDefaultModel
	}

	body, err := json.Marshal(ollamaReq{
		Model:       model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := o.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ollama: stream status %d: %s", resp.StatusCode, raw)
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		defer func() {
			_ = resp.Body.Close()
		}()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				select {
				case ch <- StreamChunk{Done: true}:
				case <-ctx.Done():
				}
				return
			}

			var chunk ollamaChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return
			}
			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta.Content
			done := chunk.Choices[0].FinishReason != nil

			select {
			case ch <- StreamChunk{ID: chunk.ID, Delta: delta, Done: done}:
			case <-ctx.Done():
				return
			}

			if done {
				return
			}
		}
	}()

	return ch, nil
}

func (o *OllamaProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama: health check build request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama: health check: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: health check status %d", resp.StatusCode)
	}
	return nil
}

func (o *OllamaProvider) CostPerInputToken(_ string) float64  { return 0.0 }
func (o *OllamaProvider) CostPerOutputToken(_ string) float64 { return 0.0 }
