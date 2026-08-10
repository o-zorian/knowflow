package retrieval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/apperror"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
)

type Result struct {
	ChunkID     string         `json:"chunk_id"`
	DocumentID  string         `json:"document_id"`
	Filename    string         `json:"filename"`
	Content     string         `json:"content"`
	TokenCount  int            `json:"token_count"`
	PageStart   *int           `json:"page_start,omitempty"`
	PageEnd     *int           `json:"page_end,omitempty"`
	HeadingPath *string        `json:"heading_path,omitempty"`
	ChunkIndex  int            `json:"chunk_index"`
	Metadata    map[string]any `json:"metadata"`
	Similarity  float64        `json:"similarity,omitempty"`
	SparseScore float64        `json:"sparse_score,omitempty"`
	RRFScore    float64        `json:"rrf_score,omitempty"`
	RerankScore *float64       `json:"rerank_score,omitempty"`
	Score       float64        `json:"score"`
}

type Response struct {
	Results           []Result `json:"results"`
	Query             string   `json:"query"`
	Strategy          string   `json:"strategy"`
	LatencyMS         int      `json:"latency_ms"`
	RerankAttempted   bool     `json:"rerank_attempted"`
	RerankFallback    bool     `json:"rerank_fallback"`
	DenseResultCount  int      `json:"dense_result_count"`
	SparseResultCount int      `json:"sparse_result_count"`
}

type Store interface {
	Config(ctx context.Context, ownerID, knowledgeBaseID string) (knowledgebase.RetrievalConfig, error)
	Dense(ctx context.Context, ownerID, knowledgeBaseID string, vector []float32, topK int) ([]Result, error)
	Sparse(ctx context.Context, ownerID, knowledgeBaseID, query string, topK int) ([]Result, error)
}

type Service struct {
	store    Store
	embedder model.Embedder
	reranker model.Reranker
}

func NewService(store Store, embedder model.Embedder, rerankers ...model.Reranker) *Service {
	var reranker model.Reranker
	if len(rerankers) > 0 {
		reranker = rerankers[0]
	}
	return &Service{store: store, embedder: embedder, reranker: reranker}
}

func (s *Service) Retrieve(ctx context.Context, ownerID, knowledgeBaseID, query string) (Response, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Response{}, apperror.New(http.StatusBadRequest, "INVALID_MESSAGE", "message content is required")
	}
	started := time.Now()
	config, err := s.store.Config(ctx, ownerID, knowledgeBaseID)
	if err != nil {
		return Response{}, mapStoreError(err)
	}

	var vector []float32
	if config.DenseTopK > 0 {
		if s.embedder == nil {
			return Response{}, apperror.New(http.StatusInternalServerError, "EMBEDDING_NOT_CONFIGURED", "question embedding is not configured")
		}
		vector, err = s.embedder.EmbedQuery(ctx, query)
		if err != nil {
			return Response{}, apperror.Wrap(http.StatusBadGateway, "EMBEDDING_FAILED", "question embedding failed", err)
		}
		if len(vector) != s.embedder.Dimension() {
			return Response{}, apperror.New(http.StatusBadGateway, "EMBEDDING_INVALID_RESPONSE", "embedding provider returned an invalid response")
		}
	}

	dense, sparse, err := s.retrieveSources(ctx, ownerID, knowledgeBaseID, query, vector, config)
	if err != nil {
		return Response{}, mapStoreError(err)
	}
	response := Response{Query: query, DenseResultCount: len(dense), SparseResultCount: len(sparse)}
	switch {
	case config.DenseTopK > 0 && config.SparseTopK > 0:
		response.Strategy = "hybrid_rrf"
		response.Results = RRF(dense, sparse, config.RRFK)
	case config.DenseTopK > 0:
		response.Strategy = "dense"
		response.Results = scoreDense(dense)
	default:
		response.Strategy = "sparse"
		response.Results = scoreSparse(sparse)
	}
	response.Results = minimumScore(response.Results, config.MinimumScore)
	if config.RerankEnabled && len(response.Results) > 0 {
		response.RerankAttempted = true
		reranked, rerankErr := s.rerank(ctx, query, response.Results, config.RerankTopK)
		if rerankErr != nil {
			response.RerankFallback = true
		} else {
			response.Results = reranked
			response.Strategy += "+rerank"
		}
	}
	if len(response.Results) > config.FinalTopK {
		response.Results = response.Results[:config.FinalTopK]
	}
	response.LatencyMS = int(time.Since(started).Milliseconds())
	return response, nil
}

type sourceResult struct {
	name    string
	results []Result
	err     error
}

func (s *Service) retrieveSources(ctx context.Context, ownerID, knowledgeBaseID, query string, vector []float32, config knowledgebase.RetrievalConfig) ([]Result, []Result, error) {
	enabled := 0
	if config.DenseTopK > 0 {
		enabled++
	}
	if config.SparseTopK > 0 {
		enabled++
	}
	outcomes := make(chan sourceResult, enabled)
	if config.DenseTopK > 0 {
		go func() {
			results, err := s.store.Dense(ctx, ownerID, knowledgeBaseID, vector, config.DenseTopK)
			outcomes <- sourceResult{name: "dense", results: results, err: err}
		}()
	}
	if config.SparseTopK > 0 {
		go func() {
			results, err := s.store.Sparse(ctx, ownerID, knowledgeBaseID, query, config.SparseTopK)
			outcomes <- sourceResult{name: "sparse", results: results, err: err}
		}()
	}
	var dense, sparse []Result
	for range enabled {
		outcome := <-outcomes
		if outcome.err != nil {
			return nil, nil, outcome.err
		}
		if outcome.name == "dense" {
			dense = outcome.results
		} else {
			sparse = outcome.results
		}
	}
	return dense, sparse, nil
}

func (s *Service) rerank(ctx context.Context, query string, candidates []Result, topK int) ([]Result, error) {
	if s.reranker == nil {
		return nil, errors.New("reranker is not configured")
	}
	limit := min(topK, len(candidates))
	documents := make([]model.RerankDocument, limit)
	for index := range limit {
		documents[index] = model.RerankDocument{ID: candidates[index].ChunkID, Content: candidates[index].Content}
	}
	ranked, err := s.reranker.Rerank(ctx, query, documents, limit)
	if err != nil {
		return nil, err
	}
	if len(ranked) == 0 {
		return nil, errors.New("reranker returned no results")
	}
	results := make([]Result, 0, len(candidates))
	seen := make(map[int]struct{}, len(ranked))
	for _, item := range ranked {
		if item.Index < 0 || item.Index >= limit || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
			return nil, errors.New("reranker returned an invalid result")
		}
		if _, duplicate := seen[item.Index]; duplicate {
			return nil, errors.New("reranker returned duplicate results")
		}
		seen[item.Index] = struct{}{}
		result := candidates[item.Index]
		score := item.Score
		result.RerankScore = &score
		result.Score = score
		results = append(results, result)
	}
	for index, candidate := range candidates {
		if _, exists := seen[index]; !exists {
			results = append(results, candidate)
		}
	}
	return results, nil
}

func RRF(dense, sparse []Result, rrfK int) []Result {
	if rrfK < 1 {
		rrfK = 60
	}
	type fused struct {
		result Result
		score  float64
	}
	byChunk := make(map[string]*fused, len(dense)+len(sparse))
	add := func(results []Result, denseSource bool) {
		for index, result := range results {
			item, exists := byChunk[result.ChunkID]
			if !exists {
				copy := result
				item = &fused{result: copy}
				byChunk[result.ChunkID] = item
			}
			item.score += 1 / float64(rrfK+index+1)
			if denseSource {
				item.result.Similarity = result.Similarity
			} else {
				item.result.SparseScore = result.SparseScore
			}
		}
	}
	add(dense, true)
	add(sparse, false)
	results := make([]Result, 0, len(byChunk))
	for _, item := range byChunk {
		item.result.RRFScore = item.score
		item.result.Score = item.score
		results = append(results, item.result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ChunkID < results[j].ChunkID
		}
		return results[i].Score > results[j].Score
	})
	return results
}

func scoreDense(results []Result) []Result {
	for index := range results {
		results[index].Score = results[index].Similarity
	}
	return results
}

func scoreSparse(results []Result) []Result {
	for index := range results {
		results[index].Score = results[index].SparseScore
	}
	return results
}

func minimumScore(results []Result, minimum float64) []Result {
	filtered := results[:0]
	for _, result := range results {
		if result.Score >= minimum {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

var (
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrNoReadyDocuments      = errors.New("knowledge base has no ready documents")
)

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, ErrKnowledgeBaseNotFound):
		return apperror.New(http.StatusNotFound, "KNOWLEDGE_BASE_NOT_FOUND", "knowledge base not found")
	case errors.Is(err, ErrNoReadyDocuments):
		return apperror.New(http.StatusConflict, "KNOWLEDGE_BASE_NOT_READY", "knowledge base has no ready documents")
	default:
		return apperror.Wrap(http.StatusInternalServerError, "RETRIEVAL_FAILED", "knowledge retrieval failed", err)
	}
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Config(ctx context.Context, ownerID, knowledgeBaseID string) (knowledgebase.RetrievalConfig, error) {
	config := knowledgebase.DefaultRetrievalConfig()
	var readyDocuments int
	err := s.pool.QueryRow(ctx, `SELECT
		COALESCE((kb.retrieval_config->>'dense_top_k')::int, $3),
		COALESCE((kb.retrieval_config->>'sparse_top_k')::int, $4),
		COALESCE((kb.retrieval_config->>'rerank_top_k')::int, $5),
		COALESCE((kb.retrieval_config->>'final_top_k')::int, $6),
		COALESCE((kb.retrieval_config->>'minimum_score')::float8, $7),
		COALESCE((kb.retrieval_config->>'rrf_k')::int, $8),
		COALESCE((kb.retrieval_config->>'rerank_enabled')::boolean, false),
		count(d.id) FILTER (WHERE d.status = 'ready' AND d.deleted_at IS NULL)
		FROM knowledge_bases kb
		LEFT JOIN documents d ON d.knowledge_base_id = kb.id
		WHERE kb.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL
		GROUP BY kb.id`, knowledgeBaseID, ownerID, config.DenseTopK, config.SparseTopK, config.RerankTopK,
		config.FinalTopK, config.MinimumScore, config.RRFK).Scan(&config.DenseTopK, &config.SparseTopK,
		&config.RerankTopK, &config.FinalTopK, &config.MinimumScore, &config.RRFK, &config.RerankEnabled, &readyDocuments)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledgebase.RetrievalConfig{}, ErrKnowledgeBaseNotFound
	}
	if err != nil {
		return knowledgebase.RetrievalConfig{}, err
	}
	if readyDocuments == 0 {
		return knowledgebase.RetrievalConfig{}, ErrNoReadyDocuments
	}
	return config, nil
}

func (s *PostgresStore) Dense(ctx context.Context, ownerID, knowledgeBaseID string, vector []float32, topK int) ([]Result, error) {
	rows, err := s.pool.Query(ctx, `SELECT dc.id::text, d.id::text, d.filename, dc.content, dc.token_count,
		dc.page_start, dc.page_end, dc.heading_path, dc.chunk_index, dc.metadata,
		1 - (dc.embedding <=> $3::vector) AS similarity
		FROM document_chunks dc
		JOIN documents d ON d.id = dc.document_id
		JOIN knowledge_bases kb ON kb.id = dc.knowledge_base_id
		WHERE dc.knowledge_base_id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL
		AND d.deleted_at IS NULL AND d.status = 'ready' AND dc.index_version = d.index_version
		ORDER BY dc.embedding <=> $3::vector, dc.id
		LIMIT $4`, knowledgeBaseID, ownerID, vectorLiteral(vector), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Result, 0, topK)
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.ChunkID, &result.DocumentID, &result.Filename, &result.Content, &result.TokenCount,
			&result.PageStart, &result.PageEnd, &result.HeadingPath, &result.ChunkIndex, &result.Metadata, &result.Similarity); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s *PostgresStore) Sparse(ctx context.Context, ownerID, knowledgeBaseID, query string, topK int) ([]Result, error) {
	rows, err := s.pool.Query(ctx, `WITH search_query AS (
		SELECT plainto_tsquery('simple', knowflow_search_terms($3)) AS query
	)
	SELECT dc.id::text, d.id::text, d.filename, dc.content, dc.token_count,
		dc.page_start, dc.page_end, dc.heading_path, dc.chunk_index, dc.metadata,
		ts_rank_cd(dc.search_vector, search_query.query, 32) AS sparse_score
		FROM document_chunks dc
		JOIN documents d ON d.id = dc.document_id
		JOIN knowledge_bases kb ON kb.id = dc.knowledge_base_id
		CROSS JOIN search_query
		WHERE dc.knowledge_base_id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL
		AND d.deleted_at IS NULL AND d.status = 'ready' AND dc.index_version = d.index_version
		AND search_query.query @@ dc.search_vector
		ORDER BY sparse_score DESC, dc.id
		LIMIT $4`, knowledgeBaseID, ownerID, query, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Result, 0, topK)
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.ChunkID, &result.DocumentID, &result.Filename, &result.Content, &result.TokenCount,
			&result.PageStart, &result.PageEnd, &result.HeadingPath, &result.ChunkIndex, &result.Metadata, &result.SparseScore); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func vectorLiteral(vector []float32) string {
	var builder strings.Builder
	builder.Grow(len(vector) * 10)
	builder.WriteByte('[')
	for index, value := range vector {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

func Location(result Result) string {
	if result.PageStart != nil {
		if result.PageEnd != nil && *result.PageEnd != *result.PageStart {
			return fmt.Sprintf("pages %d-%d", *result.PageStart, *result.PageEnd)
		}
		return fmt.Sprintf("page %d", *result.PageStart)
	}
	if result.HeadingPath != nil && strings.TrimSpace(*result.HeadingPath) != "" {
		return *result.HeadingPath
	}
	return fmt.Sprintf("paragraph/chunk %d", result.ChunkIndex+1)
}
