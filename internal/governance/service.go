package governance

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/ingestion"
	platformmetrics "knowflow/internal/platform/metrics"
)

type Service struct {
	pool    *pgxpool.Pool
	redis   redisclient.Cmdable
	metrics *platformmetrics.Metrics
}

func NewService(pool *pgxpool.Pool, redis redisclient.Cmdable, metrics *platformmetrics.Metrics) *Service {
	return &Service{pool: pool, redis: redis, metrics: metrics}
}

type Summary struct {
	Users                 int64   `json:"users"`
	KnowledgeBases        int64   `json:"knowledge_bases"`
	ReadyDocuments        int64   `json:"ready_documents"`
	FailedIngestionJobs   int64   `json:"failed_ingestion_jobs"`
	ModelCalls            int64   `json:"model_calls"`
	ModelSuccessRate      float64 `json:"model_success_rate"`
	AverageModelLatencyMS float64 `json:"average_model_latency_ms"`
	PromptTokens          int64   `json:"prompt_tokens"`
	CompletionTokens      int64   `json:"completion_tokens"`
	EstimatedCostUSD      float64 `json:"estimated_cost_usd"`
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	var result Summary
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM knowledge_bases WHERE deleted_at IS NULL),
		(SELECT count(*) FROM documents WHERE deleted_at IS NULL AND status='ready'),
		(SELECT count(*) FROM ingestion_jobs WHERE status='failed'),
		(SELECT count(*) FROM model_usage),
		COALESCE((SELECT avg((status='succeeded')::int)::float8 FROM model_usage),0),
		COALESCE((SELECT avg(latency_ms)::float8 FROM model_usage),0),
		COALESCE((SELECT sum(prompt_tokens) FROM model_usage),0),
		COALESCE((SELECT sum(completion_tokens) FROM model_usage),0),
		COALESCE((SELECT sum(estimated_cost_usd)::float8 FROM model_usage),0)`).Scan(
		&result.Users, &result.KnowledgeBases, &result.ReadyDocuments, &result.FailedIngestionJobs,
		&result.ModelCalls, &result.ModelSuccessRate, &result.AverageModelLatencyMS,
		&result.PromptTokens, &result.CompletionTokens, &result.EstimatedCostUSD)
	return result, err
}

type IngestionJob struct {
	ID           string     `json:"id"`
	DocumentID   string     `json:"document_id"`
	Filename     string     `json:"filename"`
	Status       string     `json:"status"`
	Stage        string     `json:"stage"`
	Progress     int        `json:"progress"`
	Attempts     int        `json:"attempts"`
	ErrorCode    *string    `json:"error_code,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func (s *Service) IngestionJobs(ctx context.Context, page, pageSize int, status string) ([]IngestionJob, int64, error) {
	if status == "" {
		status = "failed"
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_jobs WHERE status=$1`, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT j.id::text,j.document_id::text,d.filename,j.status,j.stage,j.progress,j.attempts,
		j.error_code,j.error_message,j.created_at,j.finished_at FROM ingestion_jobs j JOIN documents d ON d.id=j.document_id
		WHERE j.status=$1 ORDER BY j.created_at DESC LIMIT $2 OFFSET $3`, status, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []IngestionJob{}
	for rows.Next() {
		var item IngestionJob
		if err := rows.Scan(&item.ID, &item.DocumentID, &item.Filename, &item.Status, &item.Stage, &item.Progress, &item.Attempts, &item.ErrorCode, &item.ErrorMessage, &item.CreatedAt, &item.FinishedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

type ModelUsage struct {
	ID               string    `json:"id"`
	RequestType      string    `json:"request_type"`
	Model            string    `json:"model"`
	Status           string    `json:"status"`
	UserID           *string   `json:"user_id,omitempty"`
	KnowledgeBaseID  *string   `json:"knowledge_base_id,omitempty"`
	ErrorCode        *string   `json:"error_code,omitempty"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TextCount        int       `json:"text_count"`
	LatencyMS        int       `json:"latency_ms"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Service) ModelUsage(ctx context.Context, page, pageSize int) ([]ModelUsage, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,user_id::text,knowledge_base_id::text,request_type,model,prompt_tokens,
		completion_tokens,text_count,estimated_cost_usd::float8,latency_ms,status,error_code,created_at
		FROM model_usage ORDER BY created_at DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []ModelUsage{}
	for rows.Next() {
		var item ModelUsage
		if err := rows.Scan(&item.ID, &item.UserID, &item.KnowledgeBaseID, &item.RequestType, &item.Model, &item.PromptTokens, &item.CompletionTokens, &item.TextCount, &item.EstimatedCostUSD, &item.LatencyMS, &item.Status, &item.ErrorCode, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) Users(ctx context.Context, page, pageSize int) ([]User, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,email,role,status,created_at,updated_at FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []User{}
	for rows.Next() {
		var item User
		if err := rows.Scan(&item.ID, &item.Email, &item.Role, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Service) SetUserStatus(ctx context.Context, id, status string) error {
	if status != "active" && status != "disabled" {
		return errors.New("invalid user status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE users SET status=$2 WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if status == "disabled" {
		if _, err = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) RefreshOperationalMetrics(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT status,count(*) FROM ingestion_jobs GROUP BY status`)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]float64{}
	for rows.Next() {
		var status string
		var count float64
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, status := range []string{"pending", "running", "succeeded", "failed"} {
		s.metrics.SetIngestion(status, counts[status])
	}
	length, err := s.redis.LLen(ctx, ingestion.QueueKey).Result()
	if err != nil {
		return err
	}
	s.metrics.SetQueueLength(float64(length))
	return nil
}
