package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFakeRerankerOrdersByLexicalOverlap(t *testing.T) {
	reranker := &FakeReranker{}
	results, err := reranker.Rerank(context.Background(), "hybrid retrieval", []RerankDocument{
		{Content: "unrelated document"},
		{Content: "hybrid retrieval uses RRF"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Index != 1 || results[0].Score <= results[1].Score {
		t.Fatalf("results = %#v", results)
	}
}

func TestRerankClientUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/rerank" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var payload struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "rerank-test" || payload.Query != "query" || len(payload.Documents) != 2 || payload.TopN != 2 {
			t.Fatalf("payload = %#v", payload)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"results": []map[string]any{
			{"index": 1, "relevance_score": 0.9}, {"index": 0, "relevance_score": 0.2},
		}})
	}))
	defer server.Close()
	client, err := NewRerankClient(server.URL+"/v1", "secret", "rerank-test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Rerank(context.Background(), "query", []RerankDocument{{Content: "first"}, {Content: "second"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Index != 1 || results[0].Score != 0.9 {
		t.Fatalf("results = %#v", results)
	}
}
