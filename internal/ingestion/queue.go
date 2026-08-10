package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

const QueueKey = "knowflow:ingestion"

var ErrQueueEmpty = errors.New("ingestion queue is empty")

type Message struct {
	Type            string `json:"type"`
	JobID           string `json:"job_id,omitempty"`
	DocumentID      string `json:"document_id,omitempty"`
	KnowledgeBaseID string `json:"knowledge_base_id,omitempty"`
	OwnerID         string `json:"owner_id"`
	IndexVersion    int    `json:"index_version,omitempty"`
}

type RedisQueue struct{ client redisclient.Cmdable }

func NewRedisQueue(client redisclient.Cmdable) *RedisQueue { return &RedisQueue{client: client} }

func (q *RedisQueue) EnqueueIndex(ctx context.Context, ownerID, jobID, documentID string, indexVersion int) error {
	return q.enqueue(ctx, Message{Type: "document.index", OwnerID: ownerID, JobID: jobID, DocumentID: documentID, IndexVersion: indexVersion})
}

func (q *RedisQueue) EnqueueDocumentDeletion(ctx context.Context, ownerID, documentID string) error {
	return q.enqueue(ctx, Message{Type: "document.delete", OwnerID: ownerID, DocumentID: documentID})
}

func (q *RedisQueue) EnqueueKnowledgeBaseDeletion(ctx context.Context, ownerID, knowledgeBaseID string) error {
	return q.enqueue(ctx, Message{Type: "knowledge_base.delete", OwnerID: ownerID, KnowledgeBaseID: knowledgeBaseID})
}

func (q *RedisQueue) enqueue(ctx context.Context, message Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode ingestion queue message: %w", err)
	}
	if err := q.client.RPush(ctx, QueueKey, payload).Err(); err != nil {
		return fmt.Errorf("enqueue ingestion message: %w", err)
	}
	return nil
}

func (q *RedisQueue) Dequeue(ctx context.Context, timeout time.Duration) (Message, error) {
	values, err := q.client.BLPop(ctx, timeout, QueueKey).Result()
	if errors.Is(err, redisclient.Nil) {
		return Message{}, ErrQueueEmpty
	}
	if err != nil {
		return Message{}, fmt.Errorf("dequeue ingestion message: %w", err)
	}
	if len(values) != 2 {
		return Message{}, errors.New("invalid ingestion queue response")
	}
	var message Message
	if err := json.Unmarshal([]byte(values[1]), &message); err != nil {
		return Message{}, fmt.Errorf("decode ingestion message: %w", err)
	}
	if message.Type == "" {
		return Message{}, errors.New("ingestion message type is required")
	}
	return message, nil
}
