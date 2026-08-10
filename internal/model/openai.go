package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OpenAIClient struct {
	baseURL   string
	apiKey    string
	model     string
	dimension int
	client    *http.Client
}

func NewOpenAIEmbeddingClient(baseURL, apiKey, model string, dimension int, client *http.Client) (*OpenAIClient, error) {
	if dimension <= 0 {
		return nil, errors.New("embedding dimension must be positive")
	}
	configured, err := NewOpenAIClient(baseURL, apiKey, model, client)
	if err != nil {
		return nil, err
	}
	configured.dimension = dimension
	return configured, nil
}

func NewOpenAIClient(baseURL, apiKey, model string, client *http.Client) (*OpenAIClient, error) {
	baseURL, apiKey, model = strings.TrimSpace(baseURL), strings.TrimSpace(apiKey), strings.TrimSpace(model)
	if baseURL == "" || apiKey == "" || model == "" {
		return nil, errors.New("OpenAI-compatible base URL, API key, and model are required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("OpenAI-compatible base URL must be an absolute HTTP(S) URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &OpenAIClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, client: client}, nil
}

func (c *OpenAIClient) Name() string { return c.model }

func (c *OpenAIClient) Stream(ctx context.Context, request ChatRequest) (<-chan ChatEvent, error) {
	messages := make([]ChatMessage, 0, len(request.Messages)+1)
	messages = append(messages, ChatMessage{Role: "system", Content: request.SystemPrompt})
	messages = append(messages, request.Messages...)
	body, err := json.Marshal(map[string]any{
		"model": c.model, "messages": messages, "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
	})
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.authorize(httpRequest)
	response, err := c.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, providerError(response)
	}
	events := make(chan ChatEvent)
	go c.decodeChatStream(ctx, response.Body, events)
	return events, nil
}

func (c *OpenAIClient) decodeChatStream(ctx context.Context, body io.ReadCloser, events chan<- ChatEvent) {
	defer close(events)
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			sendChatEvent(ctx, events, ChatEvent{Err: fmt.Errorf("decode chat stream: %w", err)})
			return
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			if !sendChatEvent(ctx, events, ChatEvent{Delta: chunk.Choices[0].Delta.Content}) {
				return
			}
		}
		if chunk.Usage != nil && !sendChatEvent(ctx, events, ChatEvent{Usage: chunk.Usage}) {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		sendChatEvent(ctx, events, ChatEvent{Err: fmt.Errorf("read chat stream: %w", err)})
	}
}

func (c *OpenAIClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts)
}

func (c *OpenAIClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := c.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("embedding provider returned an unexpected vector count")
	}
	return vectors[0], nil
}

func (c *OpenAIClient) Dimension() int { return c.dimension }

func (c *OpenAIClient) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": c.model, "input": texts})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.authorize(request)
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerError(response)
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	vectors := make([][]float32, len(texts))
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= len(vectors) || len(item.Embedding) == 0 {
			return nil, errors.New("embedding provider returned invalid vector metadata")
		}
		vectors[item.Index] = item.Embedding
	}
	for _, vector := range vectors {
		if len(vector) == 0 {
			return nil, errors.New("embedding provider omitted a vector")
		}
		if c.dimension > 0 && len(vector) != c.dimension {
			return nil, fmt.Errorf("embedding provider returned dimension %d, want %d", len(vector), c.dimension)
		}
	}
	return vectors, nil
}

func (c *OpenAIClient) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
}

func providerError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := "provider request failed"
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		message = strings.TrimSpace(payload.Error.Message)
	}
	return fmt.Errorf("%s (status %d)", message, response.StatusCode)
}

func sendChatEvent(ctx context.Context, events chan<- ChatEvent, event ChatEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}
