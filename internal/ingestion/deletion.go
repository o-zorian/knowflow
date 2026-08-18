package ingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var ErrInvalidDeletionMessage = errors.New("invalid deletion message")

type DeletionStore interface {
	DocumentObjectKeys(ctx context.Context, ownerID, documentID string) ([]string, bool, error)
	CompleteDocumentDeletion(ctx context.Context, ownerID, documentID string) error
	KnowledgeBaseObjectKeys(ctx context.Context, ownerID, knowledgeBaseID string) ([]string, bool, error)
	CompleteKnowledgeBaseDeletion(ctx context.Context, ownerID, knowledgeBaseID string) error
}

type ObjectRemover interface {
	Remove(ctx context.Context, objectKey string) error
}

type DeletionProcessor struct {
	store   DeletionStore
	objects ObjectRemover
}

func NewDeletionProcessor(store DeletionStore, objects ObjectRemover) (*DeletionProcessor, error) {
	if store == nil || objects == nil {
		return nil, errors.New("deletion processor dependencies are required")
	}
	return &DeletionProcessor{store: store, objects: objects}, nil
}

// Process is intentionally idempotent. Object keys remain on the soft-deleted
// document rows, so a retried message can safely repeat both cleanup phases.
func (p *DeletionProcessor) Process(ctx context.Context, message Message) (bool, error) {
	if err := validateDeletionMessage(message); err != nil {
		return false, err
	}
	switch message.Type {
	case MessageDocumentDelete:
		keys, found, err := p.store.DocumentObjectKeys(ctx, message.OwnerID, message.DocumentID)
		if err != nil || !found {
			return false, err
		}
		if err := p.removeObjects(ctx, keys); err != nil {
			return false, err
		}
		if err := p.store.CompleteDocumentDeletion(ctx, message.OwnerID, message.DocumentID); err != nil {
			return false, fmt.Errorf("complete document deletion: %w", err)
		}
		return true, nil
	case MessageKnowledgeBaseDelete:
		keys, found, err := p.store.KnowledgeBaseObjectKeys(ctx, message.OwnerID, message.KnowledgeBaseID)
		if err != nil || !found {
			return false, err
		}
		if err := p.removeObjects(ctx, keys); err != nil {
			return false, err
		}
		if err := p.store.CompleteKnowledgeBaseDeletion(ctx, message.OwnerID, message.KnowledgeBaseID); err != nil {
			return false, fmt.Errorf("complete knowledge base deletion: %w", err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%w: unsupported type %q", ErrInvalidDeletionMessage, message.Type)
	}
}

func (p *DeletionProcessor) removeObjects(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if key == "" {
			return fmt.Errorf("%w: empty object key", ErrInvalidDeletionMessage)
		}
		if err := p.objects.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove object %q: %w", key, err)
		}
	}
	return nil
}

func validateDeletionMessage(message Message) error {
	if _, err := uuid.Parse(message.OwnerID); err != nil {
		return fmt.Errorf("%w: owner_id is required and must be a UUID", ErrInvalidDeletionMessage)
	}
	switch message.Type {
	case MessageDocumentDelete:
		if _, err := uuid.Parse(message.DocumentID); err != nil {
			return fmt.Errorf("%w: document_id is required and must be a UUID", ErrInvalidDeletionMessage)
		}
	case MessageKnowledgeBaseDelete:
		if _, err := uuid.Parse(message.KnowledgeBaseID); err != nil {
			return fmt.Errorf("%w: knowledge_base_id is required and must be a UUID", ErrInvalidDeletionMessage)
		}
	default:
		return fmt.Errorf("%w: unsupported type %q", ErrInvalidDeletionMessage, message.Type)
	}
	return nil
}
