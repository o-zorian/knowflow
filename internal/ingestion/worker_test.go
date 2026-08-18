package ingestion

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type oneMessageQueue struct {
	message Message
	once    sync.Once
}

func (q *oneMessageQueue) Dequeue(ctx context.Context, _ time.Duration) (Message, error) {
	var delivered bool
	q.once.Do(func() { delivered = true })
	if delivered {
		return q.message, nil
	}
	<-ctx.Done()
	return Message{}, ctx.Err()
}

type deletionCleanerStub struct {
	processed chan Message
	err       error
}

func (s *deletionCleanerStub) Process(_ context.Context, message Message) (bool, error) {
	s.processed <- message
	return s.err == nil, s.err
}

type retryQueue struct {
	*oneMessageQueue
	requeued chan Message
}

func (q *retryQueue) Requeue(_ context.Context, message Message) error {
	q.requeued <- message
	return nil
}

func TestWorkerDispatchesDocumentDeletion(t *testing.T) {
	message := Message{Type: MessageDocumentDelete, OwnerID: uuid.NewString(), DocumentID: uuid.NewString()}
	queue := &oneMessageQueue{message: message}
	cleaner := &deletionCleanerStub{processed: make(chan Message, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(queue, nil, cleaner, logger, time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case actual := <-cleaner.processed:
		if actual != message {
			t.Fatalf("unexpected deletion message: %+v", actual)
		}
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker did not dispatch deletion message")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRequeuesTransientDeletionFailure(t *testing.T) {
	message := Message{Type: MessageKnowledgeBaseDelete, OwnerID: uuid.NewString(), KnowledgeBaseID: uuid.NewString()}
	queue := &retryQueue{
		oneMessageQueue: &oneMessageQueue{message: message},
		requeued:        make(chan Message, 1),
	}
	cleaner := &deletionCleanerStub{processed: make(chan Message, 1), err: errors.New("temporary object store failure")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(queue, nil, cleaner, logger, time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case actual := <-queue.requeued:
		if actual != message {
			t.Fatalf("unexpected requeued message: %+v", actual)
		}
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker did not requeue failed deletion")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
