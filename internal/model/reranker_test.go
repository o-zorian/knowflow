package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestVikingDBRerankClientSignsAndMapsScores(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/knowledge/service/rerank" {
			t.Fatalf("unexpected request method=%q path=%q", request.Method, request.URL.Path)
		}
		authorization := request.Header.Get("Authorization")
		if authorization == "" || strings.HasPrefix(authorization, "Bearer ") || !strings.Contains(authorization, "Credential=test-ak") {
			t.Fatalf("request was not AK/SK signed: authorization=%q", authorization)
		}
		if request.Header.Get("X-Date") == "" {
			t.Fatal("signed request is missing X-Date")
		}
		var payload struct {
			Datas []struct {
				Query   any    `json:"query"`
				Content string `json:"content"`
			} `json:"datas"`
			RerankModel string `json:"rerank_model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.RerankModel != "doubao-seed-rerank" || len(payload.Datas) != 2 {
			t.Fatalf("payload = %#v", payload)
		}
		if payload.Datas[0].Query != "query" || payload.Datas[0].Content != "first" || payload.Datas[1].Content != "second" {
			t.Fatalf("datas = %#v", payload.Datas)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"scores": []float64{0.2, 0.9}, "token_usage": 12},
		})
	}))
	defer server.Close()

	client, err := NewVikingDBRerankClient(server.URL, "cn-beijing", "test-ak", "test-sk", "doubao-seed-rerank", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.SetMaxRetries(0)
	results, err := client.Rerank(context.Background(), "query", []RerankDocument{{Content: "first"}, {Content: "second"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Index != 1 || results[0].Score != 0.9 {
		t.Fatalf("results = %#v", results)
	}
}

func TestVikingDBRerankClientBatchesProviderLimit(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		var payload struct {
			Datas []json.RawMessage `json:"datas"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		scores := make([]float64, len(payload.Datas))
		if requestCount == 2 {
			scores[0] = 0.99
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"code": 0, "data": map[string]any{"scores": scores}})
	}))
	defer server.Close()

	client, err := NewVikingDBRerankClient(server.URL, "cn-beijing", "test-ak", "test-sk", "doubao-seed-rerank", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	documents := make([]RerankDocument, 51)
	results, err := client.Rerank(context.Background(), "query", documents, 1)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 || len(results) != 1 || results[0].Index != 50 || results[0].Score != 0.99 {
		t.Fatalf("requestCount=%d results=%#v", requestCount, results)
	}
}

func TestVikingDBRerankClientLive(t *testing.T) {
	if os.Getenv("VIKINGDB_LIVE_TEST") != "1" {
		t.Skip("set VIKINGDB_LIVE_TEST=1 to call the real VikingDB Rerank service")
	}
	required := []string{"VIKINGDB_AK", "VIKINGDB_SK", "VIKINGDB_HOST", "VIKINGDB_REGION", "RERANK_MODEL"}
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			t.Fatalf("%s is required for the live VikingDB test", name)
		}
	}
	client, err := NewVikingDBRerankClient(
		os.Getenv("VIKINGDB_HOST"),
		os.Getenv("VIKINGDB_REGION"),
		os.Getenv("VIKINGDB_AK"),
		os.Getenv("VIKINGDB_SK"),
		os.Getenv("RERANK_MODEL"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Rerank(context.Background(), "哪段内容解释了混合检索？", []RerankDocument{
		{Content: "混合检索同时使用稠密向量和稀疏关键词，并通过 RRF 融合排序。"},
		{Content: "办公室周五下午进行消防设备维护，与检索系统无关。"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Score < results[1].Score {
		t.Fatalf("unexpected live results: %#v", results)
	}
}
