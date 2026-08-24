package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIClientRetriesRetryableStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer server.Close()
	client, err := NewOpenAIEmbeddingClient(server.URL, "secret", "embedding-model", 2, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EmbedQuery(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestOpenAIClientStreamsDeltasAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"[1]\"}}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client, err := NewOpenAIClient(server.URL, "secret", "chat-model", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "question"}}})
	if err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	var usage *Usage
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		answer.WriteString(event.Delta)
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if answer.String() != "answer [1]" || usage == nil || usage.TotalTokens != 6 {
		t.Fatalf("answer=%q usage=%#v", answer.String(), usage)
	}
}

func TestOpenAIEmbeddingClientValidatesDimension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer server.Close()
	client, err := NewOpenAIEmbeddingClient(server.URL, "secret", "embedding-model", 3, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.EmbedQuery(context.Background(), "question")
	if err == nil || !strings.Contains(err.Error(), "dimension 2, want 3") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIEmbeddingClientRequestsConfiguredDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model          string   `json:"model"`
			Input          []string `json:"input"`
			Dimensions     int      `json:"dimensions"`
			EncodingFormat string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "embedding-model" || len(body.Input) != 1 || body.Dimensions != 2 || body.EncodingFormat != "float" {
			t.Fatalf("unexpected embedding request: %+v", body)
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer server.Close()
	client, err := NewOpenAIEmbeddingClient(server.URL, "secret", "embedding-model", 2, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EmbedQuery(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIEmbeddingVisionClientUsesMultimodalEndpoint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings/multimodal" {
			t.Fatalf("request path = %s", r.URL.Path)
		}
		var body struct {
			Model string `json:"model"`
			Input []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
			Dimensions     int    `json:"dimensions"`
			EncodingFormat string `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "doubao-embedding-vision-250615" || len(body.Input) != 1 || body.Input[0].Type != "text" || body.Dimensions != 2 || body.EncodingFormat != "float" {
			t.Fatalf("unexpected multimodal embedding request: %+v", body)
		}
		requests.Add(1)
		fmt.Fprint(w, `{"data":{"object":"embedding","embedding":[1,0]}}`)
	}))
	defer server.Close()
	client, err := NewOpenAIEmbeddingClient(server.URL, "secret", "doubao-embedding-vision-250615", 2, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := client.EmbedDocuments(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(vectors) != 2 || len(vectors[0]) != 2 || len(vectors[1]) != 2 {
		t.Fatalf("requests=%d vectors=%v", requests.Load(), vectors)
	}
}
