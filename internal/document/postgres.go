package document

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) FindDuplicate(ctx context.Context, ownerID, knowledgeBaseID, digest string) (Document, error) {
	row := s.pool.QueryRow(ctx, documentSelect+` WHERE d.knowledge_base_id = $1 AND kb.owner_id = $2
		AND kb.deleted_at IS NULL AND d.sha256 = $3 AND d.deleted_at IS NULL`, knowledgeBaseID, ownerID, digest)
	doc, err := scanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	return doc, err
}

func (s *PostgresStore) CreateDocumentAndJob(ctx context.Context, ownerID, knowledgeBaseID, objectKey string, prepared preparedUpload, documentID, jobID string) (Document, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Document{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_bases WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL)`, knowledgeBaseID, ownerID).Scan(&exists); err != nil {
		return Document{}, false, err
	}
	if !exists {
		return Document{}, false, ErrKnowledgeBaseNotFound
	}
	var doc Document
	err = tx.QueryRow(ctx, `INSERT INTO documents
		(id, knowledge_base_id, filename, mime_type, size_bytes, sha256, object_key, status, index_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'queued', 1)
		ON CONFLICT (knowledge_base_id, sha256) WHERE deleted_at IS NULL DO NOTHING
		RETURNING id::text, knowledge_base_id::text, filename, mime_type, size_bytes, sha256, status,
		chunk_count, index_version, error_code, error_message, created_at, updated_at`,
		documentID, knowledgeBaseID, prepared.filename, prepared.mimeType, prepared.size, prepared.sha256, objectKey).
		Scan(&doc.ID, &doc.KnowledgeBaseID, &doc.Filename, &doc.MIMEType, &doc.SizeBytes, &doc.SHA256,
			&doc.Status, &doc.ChunkCount, &doc.IndexVersion, &doc.ErrorCode, &doc.ErrorMessage, &doc.CreatedAt, &doc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		row := tx.QueryRow(ctx, documentSelect+` WHERE d.knowledge_base_id = $1 AND kb.owner_id = $2
			AND kb.deleted_at IS NULL AND d.sha256 = $3 AND d.deleted_at IS NULL`, knowledgeBaseID, ownerID, prepared.sha256)
		existing, queryErr := scanDocument(row)
		if queryErr != nil {
			return Document{}, false, queryErr
		}
		if err := tx.Commit(ctx); err != nil {
			return Document{}, false, err
		}
		return existing, true, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion_jobs
		(id, document_id, index_version, status, stage, progress, attempts, idempotency_key)
		VALUES ($1, $2, 1, 'pending', 'queued', 0, 0, $3)`, jobID, documentID, documentID+":1"); err != nil {
		return Document{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Document{}, false, err
	}
	return doc, false, nil
}

func (s *PostgresStore) MarkEnqueueFailed(ctx context.Context, ownerID, documentID, jobID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs j SET status = 'failed', stage = 'queue',
		error_code = 'QUEUE_UNAVAILABLE', error_message = 'indexing could not be queued', finished_at = now()
		FROM documents d JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE j.id = $1 AND j.document_id = d.id AND d.id = $2 AND kb.owner_id = $3`, jobID, documentID, ownerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE documents d SET status = 'failed', error_code = 'QUEUE_UNAVAILABLE',
		error_message = 'indexing could not be queued' FROM knowledge_bases kb
		WHERE d.id = $1 AND kb.id = d.knowledge_base_id AND kb.owner_id = $2`, documentID, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) List(ctx context.Context, ownerID, knowledgeBaseID string, page, pageSize int) (Page, error) {
	var owned bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_bases WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL)`, knowledgeBaseID, ownerID).Scan(&owned); err != nil {
		return Page{}, err
	}
	if !owned {
		return Page{}, ErrKnowledgeBaseNotFound
	}
	result := Page{Items: []Document{}, Page: page, PageSize: pageSize}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE knowledge_base_id = $1 AND deleted_at IS NULL`, knowledgeBaseID).Scan(&result.Total); err != nil {
		return Page{}, err
	}
	rows, err := s.pool.Query(ctx, documentSelect+` WHERE d.knowledge_base_id = $1 AND kb.owner_id = $2
		AND kb.deleted_at IS NULL AND d.deleted_at IS NULL ORDER BY d.created_at DESC, d.id LIMIT $3 OFFSET $4`,
		knowledgeBaseID, ownerID, pageSize, (page-1)*pageSize)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return Page{}, err
		}
		result.Items = append(result.Items, doc)
	}
	return result, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, ownerID, documentID string) (Document, error) {
	row := s.pool.QueryRow(ctx, documentSelect+` WHERE d.id = $1 AND kb.owner_id = $2
		AND kb.deleted_at IS NULL AND d.deleted_at IS NULL`, documentID, ownerID)
	doc, err := scanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	return doc, err
}

func (s *PostgresStore) ListChunks(ctx context.Context, ownerID, documentID string, page, pageSize int) (ChunkPage, error) {
	result := ChunkPage{Items: []Chunk{}, Page: page, PageSize: pageSize}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM document_chunks dc
		JOIN documents d ON d.id = dc.document_id JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL AND d.deleted_at IS NULL
		AND dc.index_version = d.index_version`, documentID, ownerID).Scan(&result.Total); err != nil {
		return ChunkPage{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT dc.id::text, dc.chunk_index, dc.content, dc.token_count,
		dc.page_start, dc.page_end, dc.heading_path, dc.metadata FROM document_chunks dc
		JOIN documents d ON d.id = dc.document_id JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL AND d.deleted_at IS NULL
		AND dc.index_version = d.index_version ORDER BY dc.chunk_index LIMIT $3 OFFSET $4`, documentID, ownerID, pageSize, (page-1)*pageSize)
	if err != nil {
		return ChunkPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var chunk Chunk
		var metadata []byte
		if err := rows.Scan(&chunk.ID, &chunk.ChunkIndex, &chunk.Content, &chunk.TokenCount, &chunk.PageStart, &chunk.PageEnd, &chunk.HeadingPath, &metadata); err != nil {
			return ChunkPage{}, err
		}
		if err := json.Unmarshal(metadata, &chunk.Metadata); err != nil {
			return ChunkPage{}, err
		}
		result.Items = append(result.Items, chunk)
	}
	return result, rows.Err()
}

func (s *PostgresStore) PrepareRetry(ctx context.Context, ownerID, documentID string) (Document, IngestionJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Document{}, IngestionJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var doc Document
	var job IngestionJob
	err = tx.QueryRow(ctx, `SELECT d.id::text, d.knowledge_base_id::text, d.filename, d.mime_type, d.size_bytes,
		d.sha256, d.status, d.chunk_count, d.index_version, d.error_code, d.error_message, d.created_at, d.updated_at,
		j.id::text, j.status, j.stage, j.progress, j.attempts, j.error_code, j.error_message, j.started_at, j.finished_at, j.created_at
		FROM documents d JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
		JOIN ingestion_jobs j ON j.document_id = d.id AND j.index_version = d.index_version
		WHERE d.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL AND d.deleted_at IS NULL FOR UPDATE OF d, j`, documentID, ownerID).
		Scan(&doc.ID, &doc.KnowledgeBaseID, &doc.Filename, &doc.MIMEType, &doc.SizeBytes, &doc.SHA256,
			&doc.Status, &doc.ChunkCount, &doc.IndexVersion, &doc.ErrorCode, &doc.ErrorMessage, &doc.CreatedAt, &doc.UpdatedAt,
			&job.ID, &job.Status, &job.Stage, &job.Progress, &job.Attempts, &job.ErrorCode, &job.ErrorMessage,
			&job.StartedAt, &job.FinishedAt, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, IngestionJob{}, ErrNotFound
	}
	if err != nil {
		return Document{}, IngestionJob{}, err
	}
	if doc.Status != StatusFailed || job.Status != "failed" {
		return Document{}, IngestionJob{}, ErrNotRetryable
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET status = 'queued', error_code = NULL, error_message = NULL WHERE id = $1`, documentID); err != nil {
		return Document{}, IngestionJob{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status = 'pending', stage = 'queued', progress = 0,
		error_code = NULL, error_message = NULL, started_at = NULL, finished_at = NULL WHERE id = $1`, job.ID); err != nil {
		return Document{}, IngestionJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Document{}, IngestionJob{}, err
	}
	doc.Status, doc.ErrorCode, doc.ErrorMessage = StatusQueued, nil, nil
	job.Status, job.Stage, job.Progress = "pending", "queued", 0
	job.ErrorCode, job.ErrorMessage, job.StartedAt, job.FinishedAt = nil, nil, nil, nil
	job.DocumentID, job.IndexVersion = doc.ID, doc.IndexVersion
	return doc, job, nil
}

func (s *PostgresStore) MarkDeleting(ctx context.Context, ownerID, documentID string) error {
	result, err := s.pool.Exec(ctx, `UPDATE documents d SET status = 'deleting' FROM knowledge_bases kb
		WHERE d.id = $1 AND kb.id = d.knowledge_base_id AND kb.owner_id = $2
		AND kb.deleted_at IS NULL AND d.deleted_at IS NULL`, documentID, ownerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const documentSelect = `SELECT d.id::text, d.knowledge_base_id::text, d.filename, d.mime_type, d.size_bytes,
	d.sha256, d.status, d.chunk_count, d.index_version, d.error_code, d.error_message, d.created_at, d.updated_at,
	j.id::text, j.status, j.stage, j.progress, j.attempts, j.error_code, j.error_message,
	j.started_at, j.finished_at, j.created_at
	FROM documents d JOIN knowledge_bases kb ON kb.id = d.knowledge_base_id
	LEFT JOIN LATERAL (SELECT * FROM ingestion_jobs ij WHERE ij.document_id = d.id
		ORDER BY ij.created_at DESC LIMIT 1) j ON true`

type scanner interface{ Scan(dest ...any) error }

func scanDocument(row scanner) (Document, error) {
	var doc Document
	var jobID, jobStatus, stage, jobErrorCode, jobErrorMessage *string
	var progress, attempts *int
	var startedAt, finishedAt, jobCreatedAt *time.Time
	err := row.Scan(&doc.ID, &doc.KnowledgeBaseID, &doc.Filename, &doc.MIMEType, &doc.SizeBytes, &doc.SHA256,
		&doc.Status, &doc.ChunkCount, &doc.IndexVersion, &doc.ErrorCode, &doc.ErrorMessage, &doc.CreatedAt, &doc.UpdatedAt,
		&jobID, &jobStatus, &stage, &progress, &attempts, &jobErrorCode, &jobErrorMessage, &startedAt, &finishedAt, &jobCreatedAt)
	if err != nil {
		return Document{}, err
	}
	if jobID != nil {
		doc.Job = &IngestionJob{ID: *jobID, DocumentID: doc.ID, IndexVersion: doc.IndexVersion,
			Status: *jobStatus, Stage: *stage, Progress: *progress, Attempts: *attempts,
			ErrorCode: jobErrorCode, ErrorMessage: jobErrorMessage, StartedAt: startedAt, FinishedAt: finishedAt, CreatedAt: *jobCreatedAt}
	}
	return doc, nil
}
