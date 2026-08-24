package retrieval

import (
	"context"
	"errors"
	"testing"

	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
)

type fakeStore struct {
	config knowledgebase.RetrievalConfig
	dense  []Result
	sparse []Result
}

func (s *fakeStore) Config(context.Context, string, string) (knowledgebase.RetrievalConfig, error) {
	return s.config, nil
}

func (s *fakeStore) Dense(context.Context, string, string, []float32, int) ([]Result, error) {
	return append([]Result(nil), s.dense...), nil
}

func (s *fakeStore) Sparse(context.Context, string, string, string, int) ([]Result, error) {
	return append([]Result(nil), s.sparse...), nil
}

func TestRRFCombinesRanksAndDeduplicatesChunks(t *testing.T) {
	results := RRF(
		[]Result{{ChunkID: "a", Similarity: 0.9}, {ChunkID: "b", Similarity: 0.8}},
		[]Result{{ChunkID: "b", SparseScore: 0.7, SparseCoverage: 0.8}, {ChunkID: "c", SparseScore: 0.6, SparseCoverage: 0.8}},
		60,
	)
	if len(results) != 3 || results[0].ChunkID != "b" {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Similarity != 0.8 || results[0].SparseScore != 0.7 || results[0].RRFScore == 0 {
		t.Fatalf("fused result = %#v", results[0])
	}
}

func TestWeightedRRFDoesNotLetWeakSparseMatchesDisplaceDenseResults(t *testing.T) {
	dense := []Result{{ChunkID: "dense-a", Similarity: 0.9}, {ChunkID: "dense-b", Similarity: 0.8}}
	sparse := []Result{{ChunkID: "unigram-noise", SparseScore: 0.9, SparseCoverage: 0.05}}
	results := RRF(dense, sparse, 60)
	if len(results) != 3 || results[0].ChunkID != "dense-a" || results[1].ChunkID != "dense-b" {
		t.Fatalf("weak sparse match displaced dense candidates: %#v", results)
	}
}

func TestFourRetrievalConfigurationsProduceResults(t *testing.T) {
	embedder, err := model.NewFakeEmbedder(1024)
	if err != nil {
		t.Fatal(err)
	}
	dense := []Result{
		{ChunkID: "a", Content: "unrelated", Similarity: 0.9},
		{ChunkID: "b", Content: "hybrid retrieval with RRF", Similarity: 0.8},
	}
	sparse := []Result{
		{ChunkID: "b", Content: "hybrid retrieval with RRF", SparseScore: 0.7},
		{ChunkID: "c", Content: "retrieval", SparseScore: 0.6},
	}
	tests := []struct {
		name       string
		denseTopK  int
		sparseTopK int
		rerank     bool
		strategy   string
	}{
		{name: "dense only", denseTopK: 20, sparseTopK: 0, strategy: "dense"},
		{name: "sparse only", denseTopK: 0, sparseTopK: 20, strategy: "sparse"},
		{name: "dense sparse RRF", denseTopK: 20, sparseTopK: 20, strategy: "hybrid_rrf"},
		{name: "dense sparse RRF reranker", denseTopK: 20, sparseTopK: 20, rerank: true, strategy: "hybrid_rrf+rerank"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := knowledgebase.DefaultRetrievalConfig()
			config.DenseTopK = test.denseTopK
			config.SparseTopK = test.sparseTopK
			config.RerankEnabled = test.rerank
			store := &fakeStore{config: config, dense: dense, sparse: sparse}
			service := NewService(store, embedder, &model.FakeReranker{})
			response, err := service.Retrieve(context.Background(), "owner", "kb", "hybrid retrieval")
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) == 0 || response.Strategy != test.strategy {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestRerankerFailureFallsBackToRRF(t *testing.T) {
	embedder, _ := model.NewFakeEmbedder(1024)
	config := knowledgebase.DefaultRetrievalConfig()
	config.RerankEnabled = true
	store := &fakeStore{config: config,
		dense:  []Result{{ChunkID: "a", Content: "dense", Similarity: 0.9}},
		sparse: []Result{{ChunkID: "b", Content: "sparse", SparseScore: 0.8}},
	}
	service := NewService(store, embedder, &model.FakeReranker{Failure: errors.New("provider unavailable")})
	response, err := service.Retrieve(context.Background(), "owner", "kb", "query")
	if err != nil {
		t.Fatal(err)
	}
	if !response.RerankAttempted || !response.RerankFallback || response.Strategy != "hybrid_rrf" || len(response.Results) != 2 {
		t.Fatalf("response = %#v", response)
	}
}
