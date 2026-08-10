package knowledgebase

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"knowflow/internal/apperror"
)

var (
	ErrNotFound   = errors.New("knowledge base not found")
	ErrNameExists = errors.New("knowledge base name exists")
)

type Store interface {
	Create(ctx context.Context, ownerID string, input CreateInput, config RetrievalConfig, dimension int) (KnowledgeBase, error)
	List(ctx context.Context, ownerID string, page, pageSize int) (Page, error)
	Get(ctx context.Context, ownerID, id string) (KnowledgeBase, error)
	Update(ctx context.Context, ownerID, id string, input UpdateInput) (KnowledgeBase, error)
	Delete(ctx context.Context, ownerID, id string) error
}

type DeletionQueue interface {
	EnqueueKnowledgeBaseDeletion(ctx context.Context, ownerID, knowledgeBaseID string) error
}

type Service struct {
	store         Store
	deletionQueue DeletionQueue
	dimension     int
}

func NewService(store Store, deletionQueue DeletionQueue, dimension int) *Service {
	return &Service{store: store, deletionQueue: deletionQueue, dimension: dimension}
}

func (s *Service) Create(ctx context.Context, ownerID string, input CreateInput) (KnowledgeBase, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.EmbeddingModel = strings.TrimSpace(input.EmbeddingModel)
	if err := validateName(input.Name); err != nil {
		return KnowledgeBase{}, err
	}
	if utf8.RuneCountInString(input.Description) > 5000 {
		return KnowledgeBase{}, apperror.New(http.StatusBadRequest, "INVALID_DESCRIPTION", "description is too long")
	}
	if input.EmbeddingModel == "" || utf8.RuneCountInString(input.EmbeddingModel) > 255 {
		return KnowledgeBase{}, apperror.New(http.StatusBadRequest, "INVALID_EMBEDDING_MODEL", "embedding_model is required")
	}
	config := DefaultRetrievalConfig()
	if input.RetrievalConfig != nil {
		config = *input.RetrievalConfig
	}
	if err := validateConfig(config); err != nil {
		return KnowledgeBase{}, err
	}
	kb, err := s.store.Create(ctx, ownerID, input, config, s.dimension)
	return kb, mapStoreError(err)
}

func (s *Service) List(ctx context.Context, ownerID string, page, pageSize int) (Page, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		return Page{}, apperror.New(http.StatusBadRequest, "INVALID_PAGE_SIZE", "page_size must not exceed 100")
	}
	result, err := s.store.List(ctx, ownerID, page, pageSize)
	return result, mapStoreError(err)
}

func (s *Service) Get(ctx context.Context, ownerID, id string) (KnowledgeBase, error) {
	kb, err := s.store.Get(ctx, ownerID, id)
	return kb, mapStoreError(err)
}

func (s *Service) Update(ctx context.Context, ownerID, id string, input UpdateInput) (KnowledgeBase, error) {
	if input.EmbeddingModel != nil {
		return KnowledgeBase{}, apperror.New(http.StatusConflict, "EMBEDDING_MODEL_IMMUTABLE", "embedding model cannot be changed through this endpoint")
	}
	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		input.Name = &trimmed
		if err := validateName(trimmed); err != nil {
			return KnowledgeBase{}, err
		}
	}
	if input.Description != nil {
		trimmed := strings.TrimSpace(*input.Description)
		input.Description = &trimmed
		if utf8.RuneCountInString(trimmed) > 5000 {
			return KnowledgeBase{}, apperror.New(http.StatusBadRequest, "INVALID_DESCRIPTION", "description is too long")
		}
	}
	if input.RetrievalConfig != nil {
		if err := validateConfig(*input.RetrievalConfig); err != nil {
			return KnowledgeBase{}, err
		}
	}
	if input.Name == nil && input.Description == nil && input.RetrievalConfig == nil {
		return KnowledgeBase{}, apperror.New(http.StatusBadRequest, "EMPTY_UPDATE", "at least one editable field is required")
	}
	kb, err := s.store.Update(ctx, ownerID, id, input)
	return kb, mapStoreError(err)
}

func (s *Service) Delete(ctx context.Context, ownerID, id string) error {
	if err := s.store.Delete(ctx, ownerID, id); err != nil {
		return mapStoreError(err)
	}
	if s.deletionQueue != nil {
		if err := s.deletionQueue.EnqueueKnowledgeBaseDeletion(ctx, ownerID, id); err != nil {
			return apperror.Wrap(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "knowledge base deletion was recorded but cleanup could not be queued", err)
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 255 {
		return apperror.New(http.StatusBadRequest, "INVALID_KNOWLEDGE_BASE_NAME", "knowledge base name must be between 1 and 255 characters")
	}
	return nil
}

func validateConfig(config RetrievalConfig) error {
	if config.ChunkSize < 100 || config.ChunkSize > 10000 || config.ChunkOverlap < 0 || config.ChunkOverlap >= config.ChunkSize ||
		config.DenseTopK < 0 || config.DenseTopK > 100 || config.SparseTopK < 0 || config.SparseTopK > 100 ||
		(config.DenseTopK == 0 && config.SparseTopK == 0) ||
		config.RerankTopK < 1 || config.RerankTopK > 100 || config.FinalTopK < 1 || config.FinalTopK > config.RerankTopK ||
		config.MinimumScore < 0 || config.MinimumScore > 1 || config.RRFK < 1 || config.RRFK > 1000 {
		return apperror.New(http.StatusBadRequest, "INVALID_RETRIEVAL_CONFIG", "retrieval_config contains invalid values")
	}
	return nil
}

func mapStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return apperror.New(http.StatusNotFound, "KNOWLEDGE_BASE_NOT_FOUND", "knowledge base not found")
	case errors.Is(err, ErrNameExists):
		return apperror.New(http.StatusConflict, "KNOWLEDGE_BASE_NAME_EXISTS", "a knowledge base with this name already exists")
	default:
		return apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
}
