package chat

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/model"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Create(ctx context.Context, userID, knowledgeBaseID, title string) (Conversation, error) {
	var conversation Conversation
	err := s.pool.QueryRow(ctx, `INSERT INTO conversations (user_id, knowledge_base_id, title)
		SELECT $1, kb.id, $3 FROM knowledge_bases kb
		WHERE kb.id = $2 AND kb.owner_id = $1 AND kb.deleted_at IS NULL
		RETURNING id::text, user_id::text, knowledge_base_id::text, title, created_at, updated_at`,
		userID, knowledgeBaseID, title).Scan(&conversation.ID, &conversation.UserID, &conversation.KnowledgeBaseID,
		&conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrKnowledgeBaseNotFound
	}
	return conversation, err
}

func (s *PostgresStore) List(ctx context.Context, userID string, page, pageSize int) (Page, error) {
	result := Page{Items: []Conversation{}, Page: page, PageSize: pageSize}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE user_id = $1`, userID).Scan(&result.Total); err != nil {
		return Page{}, err
	}
	rows, err := s.pool.Query(ctx, conversationSelect+` WHERE user_id = $1
		ORDER BY updated_at DESC, id LIMIT $2 OFFSET $3`, userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return Page{}, err
		}
		result.Items = append(result.Items, conversation)
	}
	return result, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, userID, conversationID string) (Detail, error) {
	conversation, err := scanConversation(s.pool.QueryRow(ctx, conversationSelect+` WHERE id = $1 AND user_id = $2`, conversationID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	messages, err := s.listMessages(ctx, conversationID, 0)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Conversation: conversation, Messages: messages}, nil
}

func (s *PostgresStore) Delete(ctx context.Context, userID, conversationID string) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1 AND user_id = $2`, conversationID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) StartTurn(ctx context.Context, userID, conversationID, content string) (Conversation, Message, Message, []Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	conversation, err := scanConversation(tx.QueryRow(ctx, conversationSelect+` WHERE id = $1 AND user_id = $2 FOR UPDATE`, conversationID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, Message{}, Message{}, nil, ErrNotFound
	}
	if err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	rows, err := tx.Query(ctx, messageSelect+` WHERE conversation_id = $1 ORDER BY created_at DESC, id DESC LIMIT 20`, conversationID)
	if err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	var reversed []Message
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			rows.Close()
			return Conversation{}, Message{}, Message{}, nil, scanErr
		}
		reversed = append(reversed, message)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	history := make([]Message, len(reversed))
	for index := range reversed {
		history[len(reversed)-1-index] = reversed[index]
	}
	userMessage, err := insertMessage(ctx, tx, uuid.NewString(), conversationID, "user", content, "completed")
	if err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	assistantMessage, err := insertMessage(ctx, tx, uuid.NewString(), conversationID, "assistant", "", "streaming")
	if err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE id = $1`, conversationID); err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Conversation{}, Message{}, Message{}, nil, err
	}
	return conversation, userMessage, assistantMessage, history, nil
}

func (s *PostgresStore) CompleteAssistant(ctx context.Context, messageID, content, modelName string, citations []Citation, trace map[string]any, usage model.Usage, estimatedCostUSD float64, latencyMS int) (Message, error) {
	citationJSON, err := json.Marshal(citations)
	if err != nil {
		return Message{}, err
	}
	traceJSON, err := json.Marshal(trace)
	if err != nil {
		return Message{}, err
	}
	row := s.pool.QueryRow(ctx, `UPDATE messages SET content = $2, status = 'completed', citations = $3,
		retrieval_trace = $4, model = $5, prompt_tokens = $6, completion_tokens = $7,
		estimated_cost_usd = $8, latency_ms = $9, error_code = NULL WHERE id = $1 AND role = 'assistant' AND status = 'streaming'
		RETURNING id::text, conversation_id::text, role, content, status, citations, retrieval_trace,
		model, prompt_tokens, completion_tokens, estimated_cost_usd::float8, latency_ms, error_code, created_at`,
		messageID, content, citationJSON, traceJSON, modelName, usage.PromptTokens, usage.CompletionTokens, estimatedCostUSD, latencyMS)
	return scanMessage(row)
}

func (s *PostgresStore) FailAssistant(ctx context.Context, messageID, content, code string, latencyMS int) error {
	_, err := s.pool.Exec(ctx, `UPDATE messages SET content = $2, status = 'failed', error_code = $3, latency_ms = $4
		WHERE id = $1 AND role = 'assistant' AND status = 'streaming'`, messageID, content, code, latencyMS)
	return err
}

func (s *PostgresStore) listMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	query := messageSelect + ` WHERE conversation_id = $1 ORDER BY created_at, id`
	args := []any{conversationID}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func insertMessage(ctx context.Context, tx pgx.Tx, id, conversationID, role, content, status string) (Message, error) {
	return scanMessage(tx.QueryRow(ctx, `INSERT INTO messages (id, conversation_id, role, content, status, created_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp())
		RETURNING id::text, conversation_id::text, role, content, status, citations, retrieval_trace,
		model, prompt_tokens, completion_tokens, estimated_cost_usd::float8, latency_ms, error_code, created_at`,
		id, conversationID, role, content, status))
}

const conversationSelect = `SELECT id::text, user_id::text, knowledge_base_id::text, title, created_at, updated_at FROM conversations`
const messageSelect = `SELECT id::text, conversation_id::text, role, content, status, citations, retrieval_trace,
	model, prompt_tokens, completion_tokens, estimated_cost_usd::float8, latency_ms, error_code, created_at FROM messages`

type scanner interface{ Scan(dest ...any) error }

func scanConversation(row scanner) (Conversation, error) {
	var conversation Conversation
	err := row.Scan(&conversation.ID, &conversation.UserID, &conversation.KnowledgeBaseID,
		&conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt)
	return conversation, err
}

func scanMessage(row scanner) (Message, error) {
	var message Message
	var citationJSON, traceJSON []byte
	err := row.Scan(&message.ID, &message.ConversationID, &message.Role, &message.Content, &message.Status,
		&citationJSON, &traceJSON, &message.Model, &message.PromptTokens, &message.CompletionTokens,
		&message.EstimatedCostUSD, &message.LatencyMS, &message.ErrorCode, &message.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	if err := json.Unmarshal(citationJSON, &message.Citations); err != nil {
		return Message{}, err
	}
	if err := json.Unmarshal(traceJSON, &message.RetrievalTrace); err != nil {
		return Message{}, err
	}
	message.TotalTokens = message.PromptTokens + message.CompletionTokens
	return message, nil
}
