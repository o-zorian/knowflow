package retrieval

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/apperror"
	"knowflow/internal/model"
)

type Result struct {
	ChunkID     string         `json:"chunk_id"`
	DocumentID  string         `json:"document_id"`
	Filename    string         `json:"filename"`
	Content     string         `json:"content"`
	PageStart   *int           `json:"page_start,omitempty"`
	PageEnd     *int           `json:"page_end,omitempty"`
	HeadingPath *string        `json:"heading_path,omitempty"`
	ChunkIndex  int            `json:"chunk_index"`
	Metadata    map[string]any `json:"metadata"`
	Similarity  float64        `json:"similarity"`
}

type Response struct {
	Results   []Result
	LatencyMS int
}

type Store interface {
	Dense(ctx context.Context, ownerID, knowledgeBaseID string, vector []float32) ([]Result, error)
}

type Service struct {
	store    Store
	embedder model.Embedder
}

func NewService(store Store, embedder model.Embedder) *Service {
	return &Service{store: store, embedder: embedder}
}

func (s *Service) Retrieve(ctx context.Context, ownerID, knowledgeBaseID, query string) (Response, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Response{}, apperror.New(http.StatusBadRequest, "INVALID_MESSAGE", "message content is required")
	}
	started := time.Now()
	vector, err := s.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return Response{}, apperror.Wrap(http.StatusBadGateway, "EMBEDDING_FAILED", "question embedding failed", err)
	}
	if len(vector) != s.embedder.Dimension() {
		return Response{}, apperror.New(http.StatusBadGateway, "EMBEDDING_INVALID_RESPONSE", "embedding provider returned an invalid response")
	}
	results, err := s.store.Dense(ctx, ownerID, knowledgeBaseID, vector)
	if err != nil {
		if errors.Is(err, ErrKnowledgeBaseNotFound) {
			return Response{}, apperror.New(http.StatusNotFound, "KNOWLEDGE_BASE_NOT_FOUND", "knowledge base not found")
		}
		if errors.Is(err, ErrNoReadyDocuments) {
			return Response{}, apperror.New(http.StatusConflict, "KNOWLEDGE_BASE_NOT_READY", "knowledge base has no ready documents")
		}
		return Response{}, apperror.Wrap(http.StatusInternalServerError, "RETRIEVAL_FAILED", "knowledge retrieval failed", err)
	}
	return Response{Results: results, LatencyMS: int(time.Since(started).Milliseconds())}, nil
}

var (
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrNoReadyDocuments      = errors.New("knowledge base has no ready documents")
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Dense(ctx context.Context, ownerID, knowledgeBaseID string, vector []float32) ([]Result, error) {
	var denseTopK int
	var minimumScore float64
	var readyDocuments int
	err := s.pool.QueryRow(ctx, `SELECT
		COALESCE((kb.retrieval_config->>'dense_top_k')::int, 20),
		COALESCE((kb.retrieval_config->>'minimum_score')::float8, 0),
		count(d.id) FILTER (WHERE d.status = 'ready' AND d.deleted_at IS NULL)
		FROM knowledge_bases kb
		LEFT JOIN documents d ON d.knowledge_base_id = kb.id
		WHERE kb.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL
		GROUP BY kb.id`, knowledgeBaseID, ownerID).Scan(&denseTopK, &minimumScore, &readyDocuments)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrKnowledgeBaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if readyDocuments == 0 {
		return nil, ErrNoReadyDocuments
	}
	rows, err := s.pool.Query(ctx, `SELECT dc.id::text, d.id::text, d.filename, dc.content,
		dc.page_start, dc.page_end, dc.heading_path, dc.chunk_index, dc.metadata,
		1 - (dc.embedding <=> $3::vector) AS similarity
		FROM document_chunks dc
		JOIN documents d ON d.id = dc.document_id
		JOIN knowledge_bases kb ON kb.id = dc.knowledge_base_id
		WHERE dc.knowledge_base_id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL
		AND d.deleted_at IS NULL AND d.status = 'ready' AND dc.index_version = d.index_version
		AND 1 - (dc.embedding <=> $3::vector) >= $4
		ORDER BY dc.embedding <=> $3::vector, dc.id
		LIMIT $5`, knowledgeBaseID, ownerID, vectorLiteral(vector), minimumScore, denseTopK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Result, 0, denseTopK)
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.ChunkID, &result.DocumentID, &result.Filename, &result.Content,
			&result.PageStart, &result.PageEnd, &result.HeadingPath, &result.ChunkIndex, &result.Metadata, &result.Similarity); err != nil {
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
