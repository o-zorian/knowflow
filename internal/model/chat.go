package model

import (
	"context"
	"errors"
	"strings"
)

type ChatModel interface {
	Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
}

type ChatRequest struct {
	SystemPrompt string
	Messages     []ChatMessage
	Evidence     []ChatEvidence
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatEvidence struct {
	Number  int
	Content string
}

type ChatEvent struct {
	Delta string
	Usage *Usage
	Err   error
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// FakeChatModel is deterministic and offline. It deliberately cites the first
// supplied evidence item so integration tests exercise citation validation and
// persistence without contacting a paid model.
type FakeChatModel struct {
	ModelName string
	Failure   error
}

func (f *FakeChatModel) Name() string {
	if strings.TrimSpace(f.ModelName) == "" {
		return "fake-chat"
	}
	return f.ModelName
}

func (f *FakeChatModel) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	if f.Failure != nil {
		return nil, f.Failure
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("fake chat model requires evidence")
	}
	answer := "根据知识库证据，" + strings.TrimSpace(req.Evidence[0].Content) + " [1]"
	parts := splitForStreaming(answer, 32)
	events := make(chan ChatEvent)
	go func() {
		defer close(events)
		for _, part := range parts {
			select {
			case <-ctx.Done():
				return
			case events <- ChatEvent{Delta: part}:
			}
		}
		usage := Usage{PromptTokens: estimateTokens(req.SystemPrompt + joinedMessages(req.Messages)), CompletionTokens: estimateTokens(answer)}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		select {
		case <-ctx.Done():
		case events <- ChatEvent{Usage: &usage}:
		}
	}()
	return events, nil
}

func splitForStreaming(value string, size int) []string {
	runes := []rune(value)
	var parts []string
	for len(runes) > 0 {
		end := min(size, len(runes))
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

func joinedMessages(messages []ChatMessage) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
	}
	return builder.String()
}

func estimateTokens(value string) int {
	count := len([]rune(value)) / 4
	if count == 0 && value != "" {
		return 1
	}
	return count
}
