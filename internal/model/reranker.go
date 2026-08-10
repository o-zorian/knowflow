package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Reranker interface {
	Rerank(ctx context.Context, query string, docs []RerankDocument, topK int) ([]RerankResult, error)
}

type RerankDocument struct {
	ID      string
	Content string
}

type RerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// FakeReranker is deterministic and offline. It ranks documents by lexical
// query-token overlap and keeps the original order for ties.
type FakeReranker struct {
	Failure error
}

func (f *FakeReranker) Rerank(_ context.Context, query string, docs []RerankDocument, topK int) ([]RerankResult, error) {
	if f != nil && f.Failure != nil {
		return nil, f.Failure
	}
	if topK <= 0 || len(docs) == 0 {
		return []RerankResult{}, nil
	}
	queryTokens := lexicalTokens(query)
	results := make([]RerankResult, 0, len(docs))
	for index, doc := range docs {
		docTokens := lexicalTokens(doc.Content)
		matches := 0
		for token := range queryTokens {
			if _, ok := docTokens[token]; ok {
				matches++
			}
		}
		score := 0.0
		if len(queryTokens) > 0 {
			score = float64(matches) / float64(len(queryTokens))
		}
		results = append(results, RerankResult{Index: index, Score: score})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

type RerankClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewRerankClient(baseURL, apiKey, model string, client *http.Client) (*RerankClient, error) {
	baseURL, apiKey, model = strings.TrimSpace(baseURL), strings.TrimSpace(apiKey), strings.TrimSpace(model)
	if baseURL == "" || apiKey == "" || model == "" {
		return nil, errors.New("rerank base URL, API key, and model are required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("rerank base URL must be an absolute HTTP(S) URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RerankClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, client: client}, nil
}

func (c *RerankClient) Rerank(ctx context.Context, query string, docs []RerankDocument, topK int) ([]RerankResult, error) {
	if topK <= 0 || len(docs) == 0 {
		return []RerankResult{}, nil
	}
	documents := make([]string, len(docs))
	for index, doc := range docs {
		documents[index] = doc.Content
	}
	body, err := json.Marshal(map[string]any{
		"model": c.model, "query": query, "documents": documents, "top_n": min(topK, len(documents)),
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerError(response)
	}
	var payload struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}
	results := make([]RerankResult, 0, len(payload.Results))
	seen := make(map[int]struct{}, len(payload.Results))
	for _, item := range payload.Results {
		if item.Index < 0 || item.Index >= len(docs) {
			return nil, fmt.Errorf("rerank provider returned invalid document index %d", item.Index)
		}
		if _, duplicate := seen[item.Index]; duplicate {
			return nil, fmt.Errorf("rerank provider returned duplicate document index %d", item.Index)
		}
		seen[item.Index] = struct{}{}
		results = append(results, RerankResult{Index: item.Index, Score: item.RelevanceScore})
	}
	if len(results) == 0 {
		return nil, errors.New("rerank provider returned no results")
	}
	return results, nil
}

func lexicalTokens(value string) map[string]struct{} {
	tokens := make(map[string]struct{})
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens[word.String()] = struct{}{}
			word.Reset()
		}
	}
	for _, current := range strings.ToLower(value) {
		switch {
		case unicode.Is(unicode.Han, current):
			flush()
			tokens[string(current)] = struct{}{}
		case unicode.IsLetter(current) || unicode.IsDigit(current):
			word.WriteRune(current)
		default:
			flush()
		}
	}
	flush()
	return tokens
}
