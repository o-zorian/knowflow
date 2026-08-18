package ingestion

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Consumer interface {
	Dequeue(ctx context.Context, timeout time.Duration) (Message, error)
}

type Requeuer interface {
	Requeue(ctx context.Context, message Message) error
}

type DeletionCleaner interface {
	Process(ctx context.Context, message Message) (bool, error)
}

type Worker struct {
	queue       Consumer
	processor   *Processor
	deletions   DeletionCleaner
	logger      *slog.Logger
	pollTimeout time.Duration
	jobTimeout  time.Duration
}

func NewWorker(queue Consumer, processor *Processor, deletions DeletionCleaner, logger *slog.Logger, pollTimeout, jobTimeout time.Duration) *Worker {
	return &Worker{queue: queue, processor: processor, deletions: deletions, logger: logger, pollTimeout: pollTimeout, jobTimeout: jobTimeout}
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		message, err := w.queue.Dequeue(ctx, w.pollTimeout)
		if errors.Is(err, ErrQueueEmpty) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("ingestion queue receive failed", "error", err)
			continue
		}
		switch message.Type {
		case MessageDocumentIndex:
			w.processIndex(ctx, message)
		case MessageDocumentDelete, MessageKnowledgeBaseDelete:
			w.processDeletion(ctx, message)
		default:
			w.logger.Warn("unsupported ingestion message ignored", "message_type", message.Type)
		}
	}
}

func (w *Worker) processIndex(ctx context.Context, message Message) {
	jobCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
	processed, processErr := w.processor.Process(jobCtx, message)
	cancel()
	if processErr != nil {
		processing := classify(processErr, "INDEXING_FAILED", "document indexing failed", true)
		w.logger.Error("ingestion job failed", "job_id", message.JobID, "document_id", message.DocumentID,
			"error_code", processing.Code, "retryable", processing.Retryable, "error", processErr)
		return
	}
	if processed {
		w.logger.Info("ingestion job completed", "job_id", message.JobID, "document_id", message.DocumentID)
	} else {
		w.logger.Info("duplicate ingestion job ignored", "job_id", message.JobID, "document_id", message.DocumentID)
	}
}

func (w *Worker) processDeletion(ctx context.Context, message Message) {
	jobCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
	processed, err := w.deletions.Process(jobCtx, message)
	cancel()
	if err == nil {
		if processed {
			w.logger.Info("asynchronous deletion completed", "message_type", message.Type,
				"document_id", message.DocumentID, "knowledge_base_id", message.KnowledgeBaseID)
		} else {
			w.logger.Info("deletion target no longer exists", "message_type", message.Type,
				"document_id", message.DocumentID, "knowledge_base_id", message.KnowledgeBaseID)
		}
		return
	}
	if errors.Is(err, ErrInvalidDeletionMessage) {
		w.logger.Error("invalid deletion message discarded", "message_type", message.Type, "error", err)
		return
	}
	w.logger.Error("asynchronous deletion failed", "message_type", message.Type,
		"document_id", message.DocumentID, "knowledge_base_id", message.KnowledgeBaseID, "error", err)
	w.requeueDeletion(ctx, message)
}

func (w *Worker) requeueDeletion(ctx context.Context, message Message) {
	queue, ok := w.queue.(Requeuer)
	if !ok {
		w.logger.Error("deletion message could not be retried", "message_type", message.Type, "error", "queue does not support requeue")
		return
	}
	delay := w.pollTimeout
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		if err := queue.Requeue(ctx, message); err == nil {
			w.logger.Info("deletion message requeued", "message_type", message.Type)
			return
		} else {
			w.logger.Error("requeue deletion message failed", "message_type", message.Type, "error", err)
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
