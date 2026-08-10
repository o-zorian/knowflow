package model

import (
	"context"
	"strings"
	"testing"
)

func TestFakeChatModelStreamsCitedEvidence(t *testing.T) {
	fake := &FakeChatModel{}
	events, err := fake.Stream(context.Background(), ChatRequest{
		SystemPrompt: "answer from evidence",
		Messages:     []ChatMessage{{Role: "user", Content: "question"}},
		Evidence:     []ChatEvidence{{Number: 1, Content: "KnowFlow supports streaming."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	var usage *Usage
	for event := range events {
		answer.WriteString(event.Delta)
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if !strings.Contains(answer.String(), "KnowFlow supports streaming.") || !strings.Contains(answer.String(), "[1]") {
		t.Fatalf("answer = %q", answer.String())
	}
	if usage == nil || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Fatalf("usage = %#v", usage)
	}
}
