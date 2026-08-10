package transporthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/auth"
	"knowflow/internal/document"
	"knowflow/internal/knowledgebase"
	"knowflow/migrations"
)

type integrationObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func (s *integrationObjects) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key], s.puts = data, s.puts+1
	return nil
}
func (s *integrationObjects) Remove(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}
func (s *integrationObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

type integrationQueue struct {
	mu      sync.Mutex
	indexes int
}

func (q *integrationQueue) EnqueueIndex(context.Context, string, string, string, int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.indexes++
	return nil
}
func (*integrationQueue) EnqueueDocumentDeletion(context.Context, string, string) error { return nil }
func (*integrationQueue) EnqueueKnowledgeBaseDeletion(context.Context, string, string) error {
	return nil
}

func TestM1HTTPIntegration(t *testing.T) {
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
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ToLower(uuid.NewString())
	email1, email2 := "m1-"+suffix+"-one@example.test", "m1-"+suffix+"-two@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE email = ANY($1)`, []string{email1, email2})
	})

	authService := auth.NewService(auth.NewPostgresRepository(pool), "integration-secret-at-least-32-bytes", 2*time.Hour, 24*time.Hour)
	queue := &integrationQueue{}
	objects := &integrationObjects{objects: map[string][]byte{}}
	kbService := knowledgebase.NewService(knowledgebase.NewPostgresStore(pool), queue, 1024)
	docService := document.NewService(document.NewPostgresStore(pool), objects, queue, 1<<20)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(logger, []string{"http://localhost"}, time.Second, nil, BusinessServices{
		Auth: authService, KnowledgeBase: kbService, Document: docService, MaxUploadSize: 1 << 20,
	})

	registered1 := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register", map[string]string{"email": email1, "password": "correct horse battery staple"}, "", http.StatusCreated)
	registered2 := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register", map[string]string{"email": email2, "password": "correct horse battery staple"}, "", http.StatusCreated)
	loggedIn := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email1, "password": "correct horse battery staple"}, "", http.StatusOK)
	rotated := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": loggedIn.RefreshToken}, "", http.StatusOK)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": loggedIn.RefreshToken}, "", http.StatusUnauthorized, nil)
	requestJSON(t, handler, http.MethodGet, "/api/v1/me", nil, rotated.AccessToken, http.StatusOK, nil)

	var createEnvelope struct {
		Data knowledgebase.KnowledgeBase `json:"data"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/knowledge-bases", map[string]any{"name": "Product", "description": "docs", "embedding_model": "fake-embedding"}, rotated.AccessToken, http.StatusCreated, &createEnvelope)
	kb := createEnvelope.Data
	if kb.RetrievalConfig != knowledgebase.DefaultRetrievalConfig() {
		t.Fatalf("defaults not returned: %+v", kb.RetrievalConfig)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/knowledge-bases", map[string]any{"name": "Product", "embedding_model": "fake-embedding"}, rotated.AccessToken, http.StatusConflict, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/knowledge-bases", map[string]any{"name": "Product", "embedding_model": "fake-embedding"}, registered2.AccessToken, http.StatusCreated, nil)
	requestJSON(t, handler, http.MethodGet, "/api/v1/knowledge-bases/"+kb.ID, nil, registered2.AccessToken, http.StatusNotFound, nil)

	doc, duplicate, status := uploadText(t, handler, kb.ID, rotated.AccessToken, "notes.txt", "M1 integration document")
	if status != http.StatusCreated || duplicate {
		t.Fatalf("first upload status=%d duplicate=%v", status, duplicate)
	}
	duplicateDoc, duplicate, status := uploadText(t, handler, kb.ID, rotated.AccessToken, "other-name.txt", "M1 integration document")
	if status != http.StatusOK || !duplicate || duplicateDoc.ID != doc.ID {
		t.Fatalf("duplicate response status=%d duplicate=%v doc=%s expected=%s", status, duplicate, duplicateDoc.ID, doc.ID)
	}
	if objects.puts != 1 || queue.indexes != 1 {
		t.Fatalf("duplicate persisted or queued again: puts=%d jobs=%d", objects.puts, queue.indexes)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/documents/"+doc.ID, nil, registered2.AccessToken, http.StatusNotFound, nil)
	var detail struct {
		Data document.Document `json:"data"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/documents/"+doc.ID, nil, rotated.AccessToken, http.StatusOK, &detail)
	if detail.Data.Status != document.StatusQueued || detail.Data.Job == nil || detail.Data.Job.Status != "pending" || detail.Data.Job.Stage != "queued" {
		t.Fatalf("unexpected document/job status: %+v", detail.Data)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/documents/"+doc.ID+"/chunks", nil, rotated.AccessToken, http.StatusConflict, nil)

	var documents, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT d.id), count(j.id) FROM documents d
		LEFT JOIN ingestion_jobs j ON j.document_id = d.id WHERE d.knowledge_base_id = $1`, kb.ID).Scan(&documents, &jobs); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || jobs != 1 {
		t.Fatalf("database uniqueness failed: documents=%d jobs=%d", documents, jobs)
	}

	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": rotated.RefreshToken}, "", http.StatusOK, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": rotated.RefreshToken}, "", http.StatusUnauthorized, nil)
	_ = registered1
}

func requestTokenPair(t *testing.T, handler http.Handler, method, path string, body any, access string, expected int) auth.TokenPair {
	t.Helper()
	var envelope struct {
		Data auth.TokenPair `json:"data"`
	}
	requestJSON(t, handler, method, path, body, access, expected, &envelope)
	return envelope.Data
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, access string, expected int, output any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if access != "" {
		request.Header.Set("Authorization", "Bearer "+access)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != expected {
		t.Fatalf("%s %s: status=%d expected=%d body=%s", method, path, recorder.Code, expected, recorder.Body.String())
	}
	if output != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), output); err != nil {
			t.Fatalf("decode %s %s response: %v body=%s", method, path, err, recorder.Body.String())
		}
	}
}

func uploadText(t *testing.T, handler http.Handler, knowledgeBaseID, access, filename, contents string) (document.Document, bool, int) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", "text/plain")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, contents)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+knowledgeBaseID+"/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+access)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var envelope struct {
		Data struct {
			Document  document.Document `json:"document"`
			Duplicate bool              `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode upload response: %v body=%s", err, recorder.Body.String())
	}
	return envelope.Data.Document, envelope.Data.Duplicate, recorder.Code
}
