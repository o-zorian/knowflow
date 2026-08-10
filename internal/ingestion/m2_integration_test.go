package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/document"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/migrations"
)

type integrationObjectReader struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func (s *integrationObjectReader) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *integrationObjectReader) set(key, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = []byte(content)
}

func TestM2DocumentBecomesReadyAndDuplicateTaskIsIdempotent(t *testing.T) {
	pool, cleanup := m2Database(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ownerID, documentID, jobID, objectKey := seedM2Job(t, ctx, pool, "ready")
	objects := &integrationObjectReader{objects: map[string][]byte{objectKey: []byte("# Heading\n\n" + strings.Repeat("KnowFlow indexes text. ", 120))}}
	embedder, _ := model.NewFakeEmbedder(1024)
	processor, err := NewProcessor(NewPostgresStore(pool), objects, DocumentParser{}, embedder, 4)
	if err != nil {
		t.Fatal(err)
	}
	message := Message{Type: "document.index", OwnerID: ownerID, JobID: jobID, DocumentID: documentID, IndexVersion: 1}
	processed, err := processor.Process(ctx, message)
	if err != nil || !processed {
		t.Fatalf("first processing failed: processed=%v err=%v", processed, err)
	}
	status, jobStatus, stage, progress, attempts, chunkCount, rowCount := m2State(t, ctx, pool, documentID, jobID)
	if status != "ready" || jobStatus != "succeeded" || stage != "completed" || progress != 100 || attempts != 1 || chunkCount == 0 || rowCount != chunkCount {
		t.Fatalf("unexpected ready state: doc=%s job=%s stage=%s progress=%d attempts=%d chunks=%d rows=%d", status, jobStatus, stage, progress, attempts, chunkCount, rowCount)
	}
	processed, err = processor.Process(ctx, message)
	if err != nil || processed {
		t.Fatalf("duplicate processing was not ignored: processed=%v err=%v", processed, err)
	}
	_, _, _, _, attemptsAfter, chunkCountAfter, rowCountAfter := m2State(t, ctx, pool, documentID, jobID)
	if attemptsAfter != attempts || chunkCountAfter != chunkCount || rowCountAfter != rowCount {
		t.Fatalf("duplicate task changed data: attempts=%d/%d chunk_count=%d/%d rows=%d/%d", attempts, attemptsAfter, chunkCount, chunkCountAfter, rowCount, rowCountAfter)
	}
}

func TestM2FailedDocumentCanRetryWithoutDuplicateChunks(t *testing.T) {
	pool, cleanup := m2Database(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ownerID, documentID, jobID, objectKey := seedM2Job(t, ctx, pool, "retry")
	objects := &integrationObjectReader{objects: map[string][]byte{objectKey: []byte("\x00\n\t")}}
	embedder, _ := model.NewFakeEmbedder(1024)
	processor, _ := NewProcessor(NewPostgresStore(pool), objects, DocumentParser{}, embedder, 8)
	message := Message{Type: "document.index", OwnerID: ownerID, JobID: jobID, DocumentID: documentID, IndexVersion: 1}
	if processed, err := processor.Process(ctx, message); err == nil || !processed {
		t.Fatalf("empty document did not fail: processed=%v err=%v", processed, err)
	}
	status, jobStatus, _, _, attempts, _, rows := m2State(t, ctx, pool, documentID, jobID)
	if status != "failed" || jobStatus != "failed" || attempts != 1 || rows != 0 {
		t.Fatalf("unexpected failed state: doc=%s job=%s attempts=%d rows=%d", status, jobStatus, attempts, rows)
	}
	if _, _, err := document.NewPostgresStore(pool).PrepareRetry(ctx, ownerID, documentID); err != nil {
		t.Fatal(err)
	}
	objects.set(objectKey, strings.Repeat("retry content. ", 100))
	if processed, err := processor.Process(ctx, message); err != nil || !processed {
		t.Fatalf("retry failed: processed=%v err=%v", processed, err)
	}
	status, jobStatus, _, _, attempts, chunkCount, rows := m2State(t, ctx, pool, documentID, jobID)
	if status != "ready" || jobStatus != "succeeded" || attempts != 2 || chunkCount == 0 || rows != chunkCount {
		t.Fatalf("unexpected retry state: doc=%s job=%s attempts=%d chunks=%d rows=%d", status, jobStatus, attempts, chunkCount, rows)
	}
}

func m2Database(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	databaseURL := os.Getenv("KNOWFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set KNOWFLOW_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool, pool.Close
}

func seedM2Job(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) (string, string, string, string) {
	t.Helper()
	ownerID, knowledgeBaseID, documentID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	objectKey := "integration/" + documentID
	config, _ := json.Marshal(knowledgebase.DefaultRetrievalConfig())
	email := "m2-" + uuid.NewString() + "@example.test"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'not-used')`, ownerID, email); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, ownerID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_bases
		(id, owner_id, name, embedding_model, embedding_dimension, retrieval_config)
		VALUES ($1, $2, $3, 'fake-embedding', 1024, $4)`, knowledgeBaseID, ownerID, "M2 "+suffix+uuid.NewString(), config); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%064x", 1)
	if _, err := pool.Exec(ctx, `INSERT INTO documents
		(id, knowledge_base_id, filename, mime_type, size_bytes, sha256, object_key, status, index_version)
		VALUES ($1, $2, 'sample.txt', 'text/plain', 10, $3, $4, 'queued', 1)`, documentID, knowledgeBaseID, digest, objectKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ingestion_jobs
		(id, document_id, index_version, status, stage, progress, attempts, idempotency_key)
		VALUES ($1, $2, 1, 'pending', 'queued', 0, 0, $3)`, jobID, documentID, documentID+":1"); err != nil {
		t.Fatal(err)
	}
	return ownerID, documentID, jobID, objectKey
}

func m2State(t *testing.T, ctx context.Context, pool *pgxpool.Pool, documentID, jobID string) (string, string, string, int, int, int, int) {
	t.Helper()
	var documentStatus, jobStatus, stage string
	var progress, attempts, chunkCount, rowCount int
	if err := pool.QueryRow(ctx, `SELECT d.status, j.status, j.stage, j.progress, j.attempts, d.chunk_count,
		(SELECT count(*) FROM document_chunks dc WHERE dc.document_id = d.id AND dc.index_version = d.index_version)
		FROM documents d JOIN ingestion_jobs j ON j.document_id = d.id WHERE d.id = $1 AND j.id = $2`, documentID, jobID).
		Scan(&documentStatus, &jobStatus, &stage, &progress, &attempts, &chunkCount, &rowCount); err != nil {
		t.Fatal(err)
	}
	return documentStatus, jobStatus, stage, progress, attempts, chunkCount, rowCount
}
