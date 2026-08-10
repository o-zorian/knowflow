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

type Worker struct {
	queue       Consumer
	processor   *Processor
	logger      *slog.Logger
	pollTimeout time.Duration
	jobTimeout  time.Duration
}

func NewWorker(queue Consumer, processor *Processor, logger *slog.Logger, pollTimeout, jobTimeout time.Duration) *Worker {
	return &Worker{queue: queue, processor: processor, logger: logger, pollTimeout: pollTimeout, jobTimeout: jobTimeout}
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
		if message.Type != "document.index" {
			w.logger.Warn("unsupported ingestion message ignored", "message_type", message.Type)
			continue
		}
		jobCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
		processed, processErr := w.processor.Process(jobCtx, message)
		cancel()
		if processErr != nil {
			processing := classify(processErr, "INDEXING_FAILED", "document indexing failed", true)
			w.logger.Error("ingestion job failed", "job_id", message.JobID, "document_id", message.DocumentID,
				"error_code", processing.Code, "retryable", processing.Retryable, "error", processErr)
			continue
		}
		if processed {
			w.logger.Info("ingestion job completed", "job_id", message.JobID, "document_id", message.DocumentID)
		} else {
			w.logger.Info("duplicate ingestion job ignored", "job_id", message.JobID, "document_id", message.DocumentID)
		}
	}
}
