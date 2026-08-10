package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/google/uuid"

	"knowflow/internal/apperror"
)

var (
	ErrNotFound              = errors.New("document not found")
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrNotRetryable          = errors.New("document is not retryable")
)

type Store interface {
	FindDuplicate(ctx context.Context, ownerID, knowledgeBaseID, digest string) (Document, error)
	CreateDocumentAndJob(ctx context.Context, ownerID, knowledgeBaseID, objectKey string, prepared preparedUpload, documentID, jobID string) (Document, bool, error)
	MarkEnqueueFailed(ctx context.Context, ownerID, documentID, jobID string) error
	List(ctx context.Context, ownerID, knowledgeBaseID string, page, pageSize int) (Page, error)
	Get(ctx context.Context, ownerID, documentID string) (Document, error)
	ListChunks(ctx context.Context, ownerID, documentID string, page, pageSize int) (ChunkPage, error)
	PrepareRetry(ctx context.Context, ownerID, documentID string) (Document, IngestionJob, error)
	MarkDeleting(ctx context.Context, ownerID, documentID string) error
}

type ObjectStore interface {
	Put(ctx context.Context, objectKey string, reader io.Reader, size int64, contentType string) error
	Remove(ctx context.Context, objectKey string) error
}

type Queue interface {
	EnqueueIndex(ctx context.Context, ownerID, jobID, documentID string, indexVersion int) error
	EnqueueDocumentDeletion(ctx context.Context, ownerID, documentID string) error
}

type Service struct {
	store         Store
	objects       ObjectStore
	queue         Queue
	maxUploadSize int64
}

func NewService(store Store, objects ObjectStore, queue Queue, maxUploadSize int64) *Service {
	return &Service{store: store, objects: objects, queue: queue, maxUploadSize: maxUploadSize}
}

func (s *Service) Upload(ctx context.Context, ownerID, knowledgeBaseID, filename, declaredMIME string, reader io.Reader) (Document, bool, error) {
	prepared, err := prepareUpload(reader, filename, declaredMIME, s.maxUploadSize)
	if err != nil {
		return Document{}, false, err
	}
	defer os.Remove(prepared.path)
	if existing, err := s.store.FindDuplicate(ctx, ownerID, knowledgeBaseID, prepared.sha256); err == nil {
		return existing, true, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Document{}, false, internal(err)
	}
	documentID, jobID := uuid.NewString(), uuid.NewString()
	objectKey := fmt.Sprintf("raw/%s/%s/%s", ownerID, knowledgeBaseID, documentID)
	file, err := os.Open(prepared.path)
	if err != nil {
		return Document{}, false, internal(err)
	}
	putErr := s.objects.Put(ctx, objectKey, file, prepared.size, prepared.mimeType)
	_ = file.Close()
	if putErr != nil {
		return Document{}, false, apperror.Wrap(http.StatusServiceUnavailable, "OBJECT_STORE_UNAVAILABLE", "file could not be stored", putErr)
	}
	doc, duplicate, err := s.store.CreateDocumentAndJob(ctx, ownerID, knowledgeBaseID, objectKey, prepared, documentID, jobID)
	if err != nil {
		_ = s.objects.Remove(context.WithoutCancel(ctx), objectKey)
		return Document{}, false, internal(err)
	}
	if duplicate {
		_ = s.objects.Remove(context.WithoutCancel(ctx), objectKey)
		return doc, true, nil
	}
	if err := s.queue.EnqueueIndex(ctx, ownerID, jobID, documentID, doc.IndexVersion); err != nil {
		_ = s.store.MarkEnqueueFailed(context.WithoutCancel(ctx), ownerID, documentID, jobID)
		return Document{}, false, apperror.Wrap(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "document was stored but indexing could not be queued; retry is available", err)
	}
	doc.Job = &IngestionJob{ID: jobID, DocumentID: documentID, IndexVersion: doc.IndexVersion, Status: "pending", Stage: "queued", Progress: 0}
	return doc, false, nil
}

func (s *Service) List(ctx context.Context, ownerID, knowledgeBaseID string, page, pageSize int) (Page, error) {
	page, pageSize, err := pagination(page, pageSize)
	if err != nil {
		return Page{}, err
	}
	result, err := s.store.List(ctx, ownerID, knowledgeBaseID, page, pageSize)
	return result, mapError(err)
}

func (s *Service) Get(ctx context.Context, ownerID, documentID string) (Document, error) {
	doc, err := s.store.Get(ctx, ownerID, documentID)
	return doc, mapError(err)
}

func (s *Service) Chunks(ctx context.Context, ownerID, documentID string, page, pageSize int) (ChunkPage, error) {
	doc, err := s.Get(ctx, ownerID, documentID)
	if err != nil {
		return ChunkPage{}, err
	}
	if doc.Status != StatusReady {
		return ChunkPage{}, apperror.New(http.StatusConflict, "DOCUMENT_NOT_READY", "document chunks are not available until indexing completes")
	}
	page, pageSize, err = pagination(page, pageSize)
	if err != nil {
		return ChunkPage{}, err
	}
	result, err := s.store.ListChunks(ctx, ownerID, documentID, page, pageSize)
	return result, mapError(err)
}

func (s *Service) Retry(ctx context.Context, ownerID, documentID string) (Document, error) {
	doc, job, err := s.store.PrepareRetry(ctx, ownerID, documentID)
	if err != nil {
		return Document{}, mapError(err)
	}
	if err := s.queue.EnqueueIndex(ctx, ownerID, job.ID, documentID, doc.IndexVersion); err != nil {
		_ = s.store.MarkEnqueueFailed(context.WithoutCancel(ctx), ownerID, documentID, job.ID)
		return Document{}, apperror.Wrap(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "index retry could not be queued", err)
	}
	doc.Job = &job
	return doc, nil
}

func (s *Service) Delete(ctx context.Context, ownerID, documentID string) error {
	if err := s.store.MarkDeleting(ctx, ownerID, documentID); err != nil {
		return mapError(err)
	}
	if err := s.queue.EnqueueDocumentDeletion(ctx, ownerID, documentID); err != nil {
		return apperror.Wrap(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "document deletion was recorded but cleanup could not be queued", err)
	}
	return nil
}

func pagination(page, pageSize int) (int, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		return 0, 0, apperror.New(http.StatusBadRequest, "INVALID_PAGE_SIZE", "page_size must not exceed 100")
	}
	return page, pageSize, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return apperror.New(http.StatusNotFound, "DOCUMENT_NOT_FOUND", "document not found")
	}
	if errors.Is(err, ErrKnowledgeBaseNotFound) {
		return apperror.New(http.StatusNotFound, "KNOWLEDGE_BASE_NOT_FOUND", "knowledge base not found")
	}
	if errors.Is(err, ErrNotRetryable) {
		return apperror.New(http.StatusConflict, "DOCUMENT_NOT_RETRYABLE", "only failed indexing jobs can be retried")
	}
	return internal(err)
}

func internal(err error) error {
	return apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
}
