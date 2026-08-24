package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"knowflow/internal/model"
)

type recordingRewriter struct {
	history []model.ChatMessage
	result  string
	err     error
}

func (r *recordingRewriter) Rewrite(_ context.Context, history []model.ChatMessage, _ string) (string, error) {
	r.history = append([]model.ChatMessage(nil), history...)
	return r.result, r.err
}

func TestRewriteForRetrievalUsesRecentSixCompletedMessages(t *testing.T) {
	history := make([]Message, 0, 9)
	for index := 0; index < 8; index++ {
		history = append(history, Message{Role: "user", Status: "completed", Content: fmt.Sprintf("message-%d", index)})
	}
	history = append(history, Message{Role: "assistant", Status: "failed", Content: "ignored"})
	rewriter := &recordingRewriter{result: "standalone query"}
	query, applied, fallback := rewriteForRetrieval(context.Background(), rewriter, history, "它如何工作？")
	if query != "standalone query" || !applied || fallback {
		t.Fatalf("query=%q applied=%v fallback=%v", query, applied, fallback)
	}
	if len(rewriter.history) != 6 || rewriter.history[0].Content != "message-2" || rewriter.history[5].Content != "message-7" {
		t.Fatalf("history = %#v", rewriter.history)
	}
}

func TestRewriteForRetrievalFallsBackToOriginalQuery(t *testing.T) {
	rewriter := &recordingRewriter{err: errors.New("provider unavailable")}
	query, applied, fallback := rewriteForRetrieval(context.Background(), rewriter, nil, "original query")
	if query != "original query" || applied || !fallback {
		t.Fatalf("query=%q applied=%v fallback=%v", query, applied, fallback)
	}
}

func TestMarshalNilCitationsProducesJSONArray(t *testing.T) {
	payload, err := marshalCitations(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "[]" {
		t.Fatalf("nil citations encoded as %s, want []", payload)
	}
}
