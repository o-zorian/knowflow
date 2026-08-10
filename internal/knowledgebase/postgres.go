package knowledgebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Create(ctx context.Context, ownerID string, input CreateInput, config RetrievalConfig, dimension int) (KnowledgeBase, error) {
	configBytes, err := configJSON(config)
	if err != nil {
		return KnowledgeBase{}, err
	}
	var kb KnowledgeBase
	err = s.pool.QueryRow(ctx, `INSERT INTO knowledge_bases
		(owner_id, name, description, embedding_model, embedding_dimension, retrieval_config)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, owner_id::text, name, description, embedding_model, embedding_dimension,
		retrieval_config, created_at, updated_at`, ownerID, input.Name, input.Description, input.EmbeddingModel, dimension, configBytes).
		Scan(&kb.ID, &kb.OwnerID, &kb.Name, &kb.Description, &kb.EmbeddingModel, &kb.EmbeddingDimension,
			&configBytes, &kb.CreatedAt, &kb.UpdatedAt)
	if constraint(err, "knowledge_bases_owner_name_active_unique") {
		return KnowledgeBase{}, ErrNameExists
	}
	if err != nil {
		return KnowledgeBase{}, err
	}
	if err := json.Unmarshal(configBytes, &kb.RetrievalConfig); err != nil {
		return KnowledgeBase{}, err
	}
	return kb, nil
}

func (s *PostgresStore) List(ctx context.Context, ownerID string, page, pageSize int) (Page, error) {
	result := Page{Items: []KnowledgeBase{}, Page: page, PageSize: pageSize}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_bases WHERE owner_id = $1 AND deleted_at IS NULL`, ownerID).Scan(&result.Total); err != nil {
		return Page{}, err
	}
	rows, err := s.pool.Query(ctx, knowledgeBaseSelect+` WHERE kb.owner_id = $1 AND kb.deleted_at IS NULL
		GROUP BY kb.id ORDER BY kb.updated_at DESC, kb.id LIMIT $2 OFFSET $3`, ownerID, pageSize, (page-1)*pageSize)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		kb, err := scanKnowledgeBase(rows)
		if err != nil {
			return Page{}, err
		}
		result.Items = append(result.Items, kb)
	}
	return result, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, ownerID, id string) (KnowledgeBase, error) {
	row := s.pool.QueryRow(ctx, knowledgeBaseSelect+` WHERE kb.id = $1 AND kb.owner_id = $2 AND kb.deleted_at IS NULL GROUP BY kb.id`, id, ownerID)
	kb, err := scanKnowledgeBase(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeBase{}, ErrNotFound
	}
	return kb, err
}

func (s *PostgresStore) Update(ctx context.Context, ownerID, id string, input UpdateInput) (KnowledgeBase, error) {
	var configBytes []byte
	if input.RetrievalConfig != nil {
		var err error
		configBytes, err = configJSON(*input.RetrievalConfig)
		if err != nil {
			return KnowledgeBase{}, err
		}
	}
	result, err := s.pool.Exec(ctx, `UPDATE knowledge_bases SET
		name = COALESCE($3, name), description = COALESCE($4, description),
		retrieval_config = COALESCE($5::jsonb, retrieval_config)
		WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`, id, ownerID, input.Name, input.Description, configBytes)
	if constraint(err, "knowledge_bases_owner_name_active_unique") {
		return KnowledgeBase{}, ErrNameExists
	}
	if err != nil {
		return KnowledgeBase{}, err
	}
	if result.RowsAffected() == 0 {
		return KnowledgeBase{}, ErrNotFound
	}
	return s.Get(ctx, ownerID, id)
}

func (s *PostgresStore) Delete(ctx context.Context, ownerID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE knowledge_bases SET deleted_at = now() WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`, id, ownerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET status = 'deleting' WHERE knowledge_base_id = $1 AND deleted_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const knowledgeBaseSelect = `SELECT kb.id::text, kb.owner_id::text, kb.name, kb.description, kb.embedding_model,
	kb.embedding_dimension, kb.retrieval_config, kb.created_at, kb.updated_at,
	count(DISTINCT d.id), count(dc.id) FILTER (WHERE d.status = 'ready')
	FROM knowledge_bases kb
	LEFT JOIN documents d ON d.knowledge_base_id = kb.id AND d.deleted_at IS NULL
	LEFT JOIN document_chunks dc ON dc.document_id = d.id AND dc.index_version = d.index_version`

type scanner interface{ Scan(dest ...any) error }

func scanKnowledgeBase(row scanner) (KnowledgeBase, error) {
	var kb KnowledgeBase
	var configBytes []byte
	err := row.Scan(&kb.ID, &kb.OwnerID, &kb.Name, &kb.Description, &kb.EmbeddingModel,
		&kb.EmbeddingDimension, &configBytes, &kb.CreatedAt, &kb.UpdatedAt, &kb.DocumentCount, &kb.ReadyChunkCount)
	if err != nil {
		return KnowledgeBase{}, err
	}
	if err := json.Unmarshal(configBytes, &kb.RetrievalConfig); err != nil {
		return KnowledgeBase{}, fmt.Errorf("decode retrieval config: %w", err)
	}
	return kb, nil
}

func constraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == name
}
