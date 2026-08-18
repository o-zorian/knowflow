package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/knowledgebase"
)

var ErrJobNotClaimable = errors.New("ingestion job is not claimable")

type WorkItem struct {
	JobID           string
	DocumentID      string
	KnowledgeBaseID string
	OwnerID         string
	IndexVersion    int
	Filename        string
	MIMEType        string
	ObjectKey       string
	RetrievalConfig knowledgebase.RetrievalConfig
}

type JobStore interface {
	Claim(ctx context.Context, message Message) (WorkItem, bool, error)
	UpdateStage(ctx context.Context, item WorkItem, stage, documentStatus string, progress int) error
	Complete(ctx context.Context, item WorkItem, chunks []Chunk) error
	Fail(ctx context.Context, item WorkItem, stage, code, message string) error
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) DocumentObjectKeys(ctx context.Context, ownerID, documentID string) ([]string, bool, error) {
	var objectKey string
	err := s.pool.QueryRow(ctx, `SELECT d.object_key FROM documents d
		JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND kb.owner_id = $2`, documentID, ownerID).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load document deletion target: %w", err)
	}
	return []string{objectKey}, true, nil
}

func (s *PostgresStore) CompleteDocumentDeletion(ctx context.Context, ownerID, documentID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID string
	err = tx.QueryRow(ctx, `SELECT d.id::text FROM documents d
		JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND kb.owner_id = $2 FOR UPDATE OF d`, documentID, ownerID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status = 'failed', stage = 'deleted', progress = 0,
		error_code = 'DOCUMENT_DELETED', error_message = 'document was deleted', finished_at = now()
		WHERE document_id = $1 AND status IN ('pending', 'running')`, documentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET status = 'deleting', chunk_count = 0,
		error_code = NULL, error_message = NULL, deleted_at = COALESCE(deleted_at, now()) WHERE id = $1`, documentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) KnowledgeBaseObjectKeys(ctx context.Context, ownerID, knowledgeBaseID string) ([]string, bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM knowledge_bases WHERE id = $1 AND owner_id = $2
	)`, knowledgeBaseID, ownerID).Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("load knowledge base deletion target: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT d.object_key FROM documents d
		JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE kb.id = $1 AND kb.owner_id = $2 ORDER BY d.id`, knowledgeBaseID, ownerID)
	if err != nil {
		return nil, false, fmt.Errorf("load knowledge base object keys: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, false, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return keys, true, nil
}

func (s *PostgresStore) CompleteKnowledgeBaseDeletion(ctx context.Context, ownerID, knowledgeBaseID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_bases
		WHERE id = $1 AND owner_id = $2 FOR UPDATE`, knowledgeBaseID, ownerID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM documents WHERE knowledge_base_id = $1 FOR UPDATE`, knowledgeBaseID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var documentID string
		if err := rows.Scan(&documentID); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `DELETE FROM document_chunks WHERE knowledge_base_id = $1`, knowledgeBaseID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs j SET status = 'failed', stage = 'deleted', progress = 0,
		error_code = 'KNOWLEDGE_BASE_DELETED', error_message = 'knowledge base was deleted', finished_at = now()
		FROM documents d WHERE d.id = j.document_id AND d.knowledge_base_id = $1
		AND j.status IN ('pending', 'running')`, knowledgeBaseID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET status = 'deleting', chunk_count = 0,
		error_code = NULL, error_message = NULL, deleted_at = COALESCE(deleted_at, now())
		WHERE knowledge_base_id = $1`, knowledgeBaseID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge_bases SET deleted_at = COALESCE(deleted_at, now()) WHERE id = $1`, knowledgeBaseID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Claim(ctx context.Context, message Message) (WorkItem, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WorkItem{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var item WorkItem
	var status, documentStatus string
	var retrievalConfig []byte
	err = tx.QueryRow(ctx, `SELECT j.id::text, d.id::text, d.knowledge_base_id::text, kb.owner_id::text,
		j.index_version, d.filename, d.mime_type, d.object_key, kb.retrieval_config, j.status, d.status
		FROM ingestion_jobs j JOIN documents d ON d.id = j.document_id
		JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE j.id = $1 AND j.document_id = $2 AND j.index_version = $3 AND kb.owner_id = $4
		AND d.index_version = j.index_version AND d.deleted_at IS NULL AND kb.deleted_at IS NULL FOR UPDATE OF j, d`,
		message.JobID, message.DocumentID, message.IndexVersion, message.OwnerID).
		Scan(&item.JobID, &item.DocumentID, &item.KnowledgeBaseID, &item.OwnerID, &item.IndexVersion,
			&item.Filename, &item.MIMEType, &item.ObjectKey, &retrievalConfig, &status, &documentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkItem{}, false, ErrJobNotClaimable
	}
	if err != nil {
		return WorkItem{}, false, err
	}
	if err := json.Unmarshal(retrievalConfig, &item.RetrievalConfig); err != nil {
		return WorkItem{}, false, fmt.Errorf("decode retrieval config: %w", err)
	}
	if status != "pending" || documentStatus != "queued" {
		if err := tx.Commit(ctx); err != nil {
			return WorkItem{}, false, err
		}
		return item, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status = 'running', stage = 'parsing', progress = 10,
		attempts = attempts + 1, started_at = now(), finished_at = NULL, error_code = NULL, error_message = NULL
		WHERE id = $1`, item.JobID); err != nil {
		return WorkItem{}, false, err
	}
	documentResult, err := tx.Exec(ctx, `UPDATE documents SET status = 'parsing', error_code = NULL, error_message = NULL
		WHERE id = $1 AND index_version = $2`, item.DocumentID, item.IndexVersion)
	if err != nil {
		return WorkItem{}, false, err
	}
	if documentResult.RowsAffected() != 1 {
		return WorkItem{}, false, ErrJobNotClaimable
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkItem{}, false, err
	}
	return item, true, nil
}

func (s *PostgresStore) UpdateStage(ctx context.Context, item WorkItem, stage, documentStatus string, progress int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	jobResult, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET stage = $2, progress = $3
		WHERE id = $1 AND document_id = $4 AND index_version = $5 AND status = 'running'`,
		item.JobID, stage, progress, item.DocumentID, item.IndexVersion)
	if err != nil {
		return err
	}
	if jobResult.RowsAffected() != 1 {
		return ErrJobNotClaimable
	}
	documentResult, err := tx.Exec(ctx, `UPDATE documents SET status = $3
		WHERE id = $1 AND index_version = $2 AND deleted_at IS NULL`, item.DocumentID, item.IndexVersion, documentStatus)
	if err != nil {
		return err
	}
	if documentResult.RowsAffected() != 1 {
		return ErrJobNotClaimable
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Complete(ctx context.Context, item WorkItem, chunks []Chunk) error {
	if len(chunks) == 0 {
		return errors.New("cannot complete ingestion without chunks")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobStatus string
	var currentVersion int
	err = tx.QueryRow(ctx, `SELECT j.status, d.index_version FROM ingestion_jobs j
		JOIN documents d ON d.id = j.document_id WHERE j.id = $1 AND j.document_id = $2
		AND j.index_version = $3 AND d.deleted_at IS NULL FOR UPDATE OF j, d`, item.JobID, item.DocumentID, item.IndexVersion).
		Scan(&jobStatus, &currentVersion)
	if err != nil {
		return err
	}
	if jobStatus == "succeeded" {
		return tx.Commit(ctx)
	}
	if jobStatus != "running" || currentVersion != item.IndexVersion {
		return ErrJobNotClaimable
	}
	if _, err := tx.Exec(ctx, `DELETE FROM document_chunks WHERE document_id = $1 AND index_version = $2`, item.DocumentID, item.IndexVersion); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, chunk := range chunks {
		metadata, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("encode chunk metadata: %w", err)
		}
		batch.Queue(`INSERT INTO document_chunks
			(knowledge_base_id, document_id, index_version, chunk_index, content, token_count,
			page_start, page_end, heading_path, content_hash, metadata, embedding)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11, $12::vector)`,
			item.KnowledgeBaseID, item.DocumentID, item.IndexVersion, chunk.Index, chunk.Content, chunk.TokenCount,
			chunk.PageStart, chunk.PageEnd, chunk.HeadingPath, chunk.ContentHash, metadata, vectorLiteral(chunk.Embedding))
	}
	results := tx.SendBatch(ctx, batch)
	for range chunks {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("insert document chunk: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	documentResult, err := tx.Exec(ctx, `UPDATE documents SET status = 'ready', chunk_count = $3,
		error_code = NULL, error_message = NULL WHERE id = $1 AND index_version = $2 AND deleted_at IS NULL`,
		item.DocumentID, item.IndexVersion, len(chunks))
	if err != nil {
		return err
	}
	if documentResult.RowsAffected() != 1 {
		return ErrJobNotClaimable
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status = 'succeeded', stage = 'completed', progress = 100,
		error_code = NULL, error_message = NULL, finished_at = now() WHERE id = $1`, item.JobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Fail(ctx context.Context, item WorkItem, stage, code, message string) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status = 'failed', stage = $2,
		error_code = $3, error_message = $4, finished_at = now()
		WHERE id = $1 AND document_id = $5 AND index_version = $6 AND status = 'running'`,
		item.JobID, stage, code, message, item.DocumentID, item.IndexVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET status = 'failed', error_code = $3, error_message = $4
		WHERE id = $1 AND index_version = $2 AND deleted_at IS NULL`, item.DocumentID, item.IndexVersion, code, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
