package transporthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/auth"
	"knowflow/internal/chat"
	"knowflow/internal/document"
	"knowflow/internal/ingestion"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/internal/retrieval"
	"knowflow/migrations"
)

func TestM3UploadedDocumentProducesStreamedAnswerWithRealChunkCitation(t *testing.T) {
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

	email := "m3-" + uuid.NewString() + "@example.test"
	otherEmail := "m3-other-" + uuid.NewString() + "@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE email = ANY($1)`, []string{email, otherEmail})
	})
	objects := &integrationObjects{objects: map[string][]byte{}}
	queue := &integrationQueue{}
	authService := auth.NewService(auth.NewPostgresRepository(pool), "integration-secret-at-least-32-bytes", 2*time.Hour, 24*time.Hour)
	kbService := knowledgebase.NewService(knowledgebase.NewPostgresStore(pool), queue, 1024)
	docService := document.NewService(document.NewPostgresStore(pool), objects, queue, 1<<20)
	embedder, _ := model.NewFakeEmbedder(1024)
	retrievalService := retrieval.NewService(retrieval.NewPostgresStore(pool), embedder)
	fakeChat := &model.FakeChatModel{ModelName: "fake-chat"}
	chatService := chat.NewService(chat.NewPostgresStore(pool), retrievalService, fakeChat, fakeChat.Name(), &model.FakeQueryRewriter{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(logger, []string{"http://localhost"}, time.Second, nil, BusinessServices{
		Auth: authService, KnowledgeBase: kbService, Document: docService, Chat: chatService, MaxUploadSize: 1 << 20,
	})

	tokens := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": email, "password": "correct horse battery staple"}, "", http.StatusCreated)
	otherTokens := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": otherEmail, "password": "correct horse battery staple"}, "", http.StatusCreated)
	var kbEnvelope struct {
		Data knowledgebase.KnowledgeBase `json:"data"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/knowledge-bases", map[string]any{
		"name": "M3 demo", "embedding_model": "fake-embedding",
	}, tokens.AccessToken, http.StatusCreated, &kbEnvelope)
	documentText := "KnowFlow 的 M3 里程碑使用 SSE 流式返回答案，并且引用来自数据库中真实保存的文档分块。"
	doc, duplicate, status := uploadText(t, handler, kbEnvelope.Data.ID, tokens.AccessToken, "m3-demo.txt", documentText)
	if status != http.StatusCreated || duplicate {
		t.Fatalf("upload status=%d duplicate=%v", status, duplicate)
	}
	var jobID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM ingestion_jobs WHERE document_id = $1`, doc.ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	processor, err := ingestion.NewProcessor(ingestion.NewPostgresStore(pool), objects, ingestion.DocumentParser{}, embedder, 8)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := processor.Process(ctx, ingestion.Message{Type: "document.index", OwnerID: tokens.User.ID,
		JobID: jobID, DocumentID: doc.ID, IndexVersion: 1})
	if err != nil || !processed {
		t.Fatalf("indexing processed=%v err=%v", processed, err)
	}
	var chunkID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index LIMIT 1`, doc.ID).Scan(&chunkID); err != nil {
		t.Fatal(err)
	}

	var conversationEnvelope struct {
		Data chat.Conversation `json:"data"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/conversations", map[string]string{
		"knowledge_base_id": kbEnvelope.Data.ID, "title": "M3 acceptance",
	}, tokens.AccessToken, http.StatusCreated, &conversationEnvelope)
	requestJSON(t, handler, http.MethodPost, "/api/v1/conversations", map[string]string{
		"knowledge_base_id": kbEnvelope.Data.ID, "title": "cross-owner attempt",
	}, otherTokens.AccessToken, http.StatusNotFound, nil)
	requestJSON(t, handler, http.MethodGet, "/api/v1/conversations/"+conversationEnvelope.Data.ID, nil,
		otherTokens.AccessToken, http.StatusNotFound, nil)
	body, _ := json.Marshal(map[string]string{"content": "M3 如何返回答案和引用？"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversationEnvelope.Data.ID+"/messages", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE status=%d type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	stream := response.Body.String()
	for _, event := range []string{"message.started", "retrieval.completed", "message.delta", "citation", "usage", "message.completed"} {
		if !strings.Contains(stream, "event: "+event+"\n") {
			t.Fatalf("SSE missing %s: %s", event, stream)
		}
	}
	if !strings.Contains(stream, `"chunk_id":"`+chunkID+`"`) || !strings.Contains(stream, `"document_id":"`+doc.ID+`"`) {
		t.Fatalf("SSE does not contain persisted chunk/document citation: %s", stream)
	}

	var detailEnvelope struct {
		Data chat.Detail `json:"data"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/conversations/"+conversationEnvelope.Data.ID, nil,
		tokens.AccessToken, http.StatusOK, &detailEnvelope)
	if len(detailEnvelope.Data.Messages) != 2 {
		t.Fatalf("messages = %#v", detailEnvelope.Data.Messages)
	}
	answer := detailEnvelope.Data.Messages[1]
	if answer.Status != "completed" || !strings.Contains(answer.Content, "[1]") || len(answer.Citations) != 1 ||
		answer.Citations[0].ChunkID != chunkID || answer.Citations[0].Excerpt != documentText {
		t.Fatalf("persisted answer = %#v", answer)
	}

	followUpBody, _ := json.Marshal(map[string]string{"content": "它如何返回真实引用？"})
	followUpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversationEnvelope.Data.ID+"/messages", bytes.NewReader(followUpBody))
	followUpRequest.Header.Set("Content-Type", "application/json")
	followUpRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	followUpResponse := httptest.NewRecorder()
	handler.ServeHTTP(followUpResponse, followUpRequest)
	if followUpResponse.Code != http.StatusOK || !strings.Contains(followUpResponse.Body.String(), `"rewrite_applied":true`) ||
		!strings.Contains(followUpResponse.Body.String(), `"retrieval_query":"M3 如何返回答案和引用？；后续问题：它如何返回真实引用？"`) {
		t.Fatalf("follow-up rewrite SSE status=%d body=%s", followUpResponse.Code, followUpResponse.Body.String())
	}

	failingModel := &model.FakeChatModel{ModelName: "fake-chat", Failure: errors.New("provider unavailable")}
	failingChat := chat.NewService(chat.NewPostgresStore(pool), retrievalService, failingModel, failingModel.Name())
	failingHandler := NewHandler(logger, []string{"http://localhost"}, time.Second, nil, BusinessServices{
		Auth: authService, KnowledgeBase: kbService, Document: docService, Chat: failingChat, MaxUploadSize: 1 << 20,
	})
	var failedConversation struct {
		Data chat.Conversation `json:"data"`
	}
	requestJSON(t, failingHandler, http.MethodPost, "/api/v1/conversations", map[string]string{
		"knowledge_base_id": kbEnvelope.Data.ID, "title": "failure persistence",
	}, tokens.AccessToken, http.StatusCreated, &failedConversation)
	failureRequest := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+failedConversation.Data.ID+"/messages", bytes.NewReader(body))
	failureRequest.Header.Set("Content-Type", "application/json")
	failureRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	failureResponse := httptest.NewRecorder()
	failingHandler.ServeHTTP(failureResponse, failureRequest)
	if failureResponse.Code != http.StatusOK || !strings.Contains(failureResponse.Body.String(), "event: error\n") ||
		!strings.Contains(failureResponse.Body.String(), `"code":"MODEL_CALL_FAILED"`) {
		t.Fatalf("failure SSE status=%d body=%s", failureResponse.Code, failureResponse.Body.String())
	}
	var failedDetail struct {
		Data chat.Detail `json:"data"`
	}
	requestJSON(t, failingHandler, http.MethodGet, "/api/v1/conversations/"+failedConversation.Data.ID, nil,
		tokens.AccessToken, http.StatusOK, &failedDetail)
	if len(failedDetail.Data.Messages) != 2 || failedDetail.Data.Messages[1].Status != "failed" ||
		failedDetail.Data.Messages[1].ErrorCode == nil || *failedDetail.Data.Messages[1].ErrorCode != "MODEL_CALL_FAILED" {
		t.Fatalf("failed messages = %#v", failedDetail.Data.Messages)
	}
}
