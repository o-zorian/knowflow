package usage

import (
	"context"
	"testing"

	"knowflow/internal/model"
)

type memoryRecorder struct{ entries chan Entry }

func (r *memoryRecorder) Record(_ context.Context, entry Entry) error { r.entries <- entry; return nil }

func TestPricingCalculatesConfiguredRates(t *testing.T) {
	p := Pricing{ChatInputPerMillion: 2, ChatOutputPerMillion: 4, EmbeddingPerMillion: 1, RerankInputPerMillion: 3}
	if got := p.Chat(1_000_000, 500_000); got != 4 {
		t.Fatalf("chat cost = %v", got)
	}
	if got := p.Embedding(2_000_000); got != 2 {
		t.Fatalf("embedding cost = %v", got)
	}
	if got := p.Rerank(1_000_000); got != 3 {
		t.Fatalf("rerank cost = %v", got)
	}
}

func TestObservedModelsRecordEveryCallType(t *testing.T) {
	recorder := &memoryRecorder{entries: make(chan Entry, 3)}
	pricing := Pricing{ChatInputPerMillion: 1, ChatOutputPerMillion: 1, EmbeddingPerMillion: 1, RerankInputPerMillion: 1}
	ctx := WithScope(context.Background(), "user", "kb")
	embedder, _ := model.NewFakeEmbedder(8)
	observedEmbedder := ObserveEmbedder(embedder, "fake-embedding", recorder, pricing, nil)
	if _, err := observedEmbedder.EmbedQuery(ctx, "question"); err != nil {
		t.Fatal(err)
	}
	observedReranker := ObserveReranker(&model.FakeReranker{}, "fake-reranker", recorder, pricing, nil)
	if _, err := observedReranker.Rerank(ctx, "question", []model.RerankDocument{{Content: "question answer"}}, 1); err != nil {
		t.Fatal(err)
	}
	observedChat := ObserveChat(&model.FakeChatModel{ModelName: "fake-chat"}, "fake-chat", recorder, pricing, nil)
	events, err := observedChat.Stream(ctx, model.ChatRequest{SystemPrompt: "system", Messages: []model.ChatMessage{{Role: "user", Content: "question"}}, Evidence: []model.ChatEvidence{{Number: 1, Content: "answer"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	types := map[string]bool{}
	for range 3 {
		entry := <-recorder.entries
		types[entry.RequestType] = true
		if entry.UserID != "user" || entry.KnowledgeBaseID != "kb" || entry.Status != "succeeded" {
			t.Fatalf("entry=%#v", entry)
		}
	}
	for _, kind := range []string{"embedding", "rerank", "chat"} {
		if !types[kind] {
			t.Errorf("missing %s", kind)
		}
	}
}
