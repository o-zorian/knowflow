package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/migrations"
)

func TestM4PostgresFourConfigurationsAndRerankFallback(t *testing.T) {
	databaseURL := os.Getenv("KNOWFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set KNOWFLOW_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	userID, knowledgeBaseID, documentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	email := "m4-" + uuid.NewString() + "@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})
	embedder, err := model.NewFakeEmbedder(1024)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embedder.EmbedDocuments(ctx, []string{
		"KnowFlow 混合检索使用 RRF 融合向量与全文结果。 Hybrid retrieval combines dense and sparse results.",
		"A completely unrelated document chunk.",
	})
	if err != nil {
		t.Fatal(err)
	}
	config := knowledgebase.DefaultRetrievalConfig()
	configJSON, _ := json.Marshal(config)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'integration')`, userID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_bases
		(id, owner_id, name, embedding_model, embedding_dimension, retrieval_config)
		VALUES ($1, $2, 'M4 integration', 'fake-embedding', 1024, $3)`, knowledgeBaseID, userID, configJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO documents
		(id, knowledge_base_id, filename, mime_type, size_bytes, sha256, object_key, status, chunk_count, index_version)
		VALUES ($1, $2, 'm4.txt', 'text/plain', 1, $3, $4, 'ready', 2, 1)`,
		documentID, knowledgeBaseID, strings.Repeat("0", 64), "m4/"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	contents := []string{
		"KnowFlow 混合检索使用 RRF 融合向量与全文结果。 Hybrid retrieval combines dense and sparse results.",
		"A completely unrelated document chunk.",
	}
	for index, content := range contents {
		if _, err := pool.Exec(ctx, `INSERT INTO document_chunks
			(knowledge_base_id, document_id, index_version, chunk_index, content, token_count, content_hash, embedding)
			VALUES ($1, $2, 1, $3, $4, 10, $5, $6::vector)`, knowledgeBaseID, documentID, index,
			content, strings.Repeat("0", 64), vectorLiteral(vectors[index])); err != nil {
			t.Fatal(err)
		}
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
			configured := knowledgebase.DefaultRetrievalConfig()
			configured.DenseTopK, configured.SparseTopK = test.denseTopK, test.sparseTopK
			configured.RerankEnabled = test.rerank
			payload, _ := json.Marshal(configured)
			if _, err := pool.Exec(ctx, `UPDATE knowledge_bases SET retrieval_config = $2 WHERE id = $1`, knowledgeBaseID, payload); err != nil {
				t.Fatal(err)
			}
			service := NewService(NewPostgresStore(pool), embedder, &model.FakeReranker{})
			response, err := service.Retrieve(ctx, userID, knowledgeBaseID, "混合检索 RRF")
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) == 0 || response.Strategy != test.strategy {
				t.Fatalf("response = %#v", response)
			}
		})
	}

	// Natural-language Chinese questions contain terms that need not appear
	// verbatim in the source. Sparse retrieval must use weighted OR recall
	// rather than requiring every generated unigram and bigram to match.
	sparseResults, err := NewPostgresStore(pool).Sparse(ctx, userID, knowledgeBaseID, "混合检索当前版本何时生效", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sparseResults) == 0 || sparseResults[0].DocumentID != documentID || !strings.Contains(sparseResults[0].Content, "混合检索") {
		t.Fatalf("natural-language CJK sparse results = %#v", sparseResults)
	}

	config.RerankEnabled = true
	payload, _ := json.Marshal(config)
	if _, err := pool.Exec(ctx, `UPDATE knowledge_bases SET retrieval_config = $2 WHERE id = $1`, knowledgeBaseID, payload); err != nil {
		t.Fatal(err)
	}
	failing := NewService(NewPostgresStore(pool), embedder, &model.FakeReranker{Failure: errors.New("provider unavailable")})
	response, err := failing.Retrieve(ctx, userID, knowledgeBaseID, "混合检索 RRF")
	if err != nil {
		t.Fatal(err)
	}
	if !response.RerankFallback || len(response.Results) == 0 {
		t.Fatalf("fallback response = %#v", response)
	}
}
