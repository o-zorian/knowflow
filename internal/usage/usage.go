package usage

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/model"
	"knowflow/internal/platform/requestid"
)

type Pricing struct {
	ChatInputPerMillion   float64 `json:"chat_input_per_million_usd"`
	ChatOutputPerMillion  float64 `json:"chat_output_per_million_usd"`
	EmbeddingPerMillion   float64 `json:"embedding_per_million_usd"`
	RerankInputPerMillion float64 `json:"rerank_input_per_million_usd"`
}

func (p Pricing) Chat(prompt, completion int) float64 {
	return float64(prompt)*p.ChatInputPerMillion/1_000_000 + float64(completion)*p.ChatOutputPerMillion/1_000_000
}

func (p Pricing) Embedding(tokens int) float64 {
	return float64(tokens) * p.EmbeddingPerMillion / 1_000_000
}

func (p Pricing) Rerank(tokens int) float64 {
	return float64(tokens) * p.RerankInputPerMillion / 1_000_000
}

type Entry struct {
	UserID, KnowledgeBaseID                   string
	RequestType, Model, Status, ErrorCode     string
	PromptTokens, CompletionTokens, TextCount int
	EstimatedCostUSD                          float64
	LatencyMS                                 int
}

type Recorder interface {
	Record(context.Context, Entry) error
}

type Observer interface{ ObserveModel(Entry) }

type PostgresRecorder struct{ pool *pgxpool.Pool }

func NewPostgresRecorder(pool *pgxpool.Pool) *PostgresRecorder { return &PostgresRecorder{pool: pool} }

func (r *PostgresRecorder) Record(ctx context.Context, entry Entry) error {
	var userID, knowledgeBaseID any
	if entry.UserID != "" {
		userID = entry.UserID
	}
	if entry.KnowledgeBaseID != "" {
		knowledgeBaseID = entry.KnowledgeBaseID
	}
	var requestID any
	if id := requestid.FromContext(ctx); id != "" {
		requestID = id
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO model_usage
		(user_id, knowledge_base_id, request_id, trace_id, request_type, model, prompt_tokens,
		 completion_tokens, text_count, estimated_cost_usd, latency_ms, status, error_code)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''))`,
		userID, knowledgeBaseID, requestID, uuid.NewString(), entry.RequestType, entry.Model,
		entry.PromptTokens, entry.CompletionTokens, entry.TextCount, entry.EstimatedCostUSD,
		entry.LatencyMS, entry.Status, entry.ErrorCode)
	return err
}

type contextMetadata struct{ userID, knowledgeBaseID string }
type metadataKey struct{}

func WithScope(ctx context.Context, userID, knowledgeBaseID string) context.Context {
	return context.WithValue(ctx, metadataKey{}, contextMetadata{userID: userID, knowledgeBaseID: knowledgeBaseID})
}

func scopeFrom(ctx context.Context) contextMetadata {
	value, _ := ctx.Value(metadataKey{}).(contextMetadata)
	return value
}

type ObservedEmbedder struct {
	next     model.Embedder
	name     string
	recorder Recorder
	pricing  Pricing
	observer Observer
}

func ObserveEmbedder(next model.Embedder, name string, recorder Recorder, pricing Pricing, observer Observer) model.Embedder {
	return &ObservedEmbedder{next: next, name: name, recorder: recorder, pricing: pricing, observer: observer}
}

func (o *ObservedEmbedder) Dimension() int { return o.next.Dimension() }

func (o *ObservedEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	started := time.Now()
	vectors, err := o.next.EmbedDocuments(ctx, texts)
	o.record(ctx, texts, started, err)
	return vectors, err
}

func (o *ObservedEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	started := time.Now()
	vector, err := o.next.EmbedQuery(ctx, text)
	o.record(ctx, []string{text}, started, err)
	return vector, err
}

func (o *ObservedEmbedder) record(ctx context.Context, texts []string, started time.Time, callErr error) {
	tokens := estimateTexts(texts)
	o.persist(ctx, Entry{RequestType: "embedding", Model: o.name, PromptTokens: tokens, TextCount: len(texts),
		EstimatedCostUSD: o.pricing.Embedding(tokens), LatencyMS: elapsedMS(started), Status: status(callErr), ErrorCode: errorCode(callErr)})
}

type ObservedReranker struct {
	next     model.Reranker
	name     string
	recorder Recorder
	pricing  Pricing
	observer Observer
}

func ObserveReranker(next model.Reranker, name string, recorder Recorder, pricing Pricing, observer Observer) model.Reranker {
	if next == nil {
		return nil
	}
	return &ObservedReranker{next: next, name: name, recorder: recorder, pricing: pricing, observer: observer}
}

func (o *ObservedReranker) Rerank(ctx context.Context, query string, docs []model.RerankDocument, topK int) ([]model.RerankResult, error) {
	started := time.Now()
	texts := make([]string, 0, len(docs)+1)
	texts = append(texts, query)
	for _, doc := range docs {
		texts = append(texts, doc.Content)
	}
	result, err := o.next.Rerank(ctx, query, docs, topK)
	tokens := estimateTexts(texts)
	o.persist(ctx, Entry{RequestType: "rerank", Model: o.name, PromptTokens: tokens, TextCount: len(docs),
		EstimatedCostUSD: o.pricing.Rerank(tokens), LatencyMS: elapsedMS(started), Status: status(err), ErrorCode: errorCode(err)})
	return result, err
}

type ObservedChatModel struct {
	next     model.ChatModel
	name     string
	recorder Recorder
	pricing  Pricing
	observer Observer
}

func ObserveChat(next model.ChatModel, name string, recorder Recorder, pricing Pricing, observer Observer) model.ChatModel {
	return &ObservedChatModel{next: next, name: name, recorder: recorder, pricing: pricing, observer: observer}
}

func (o *ObservedChatModel) Stream(ctx context.Context, request model.ChatRequest) (<-chan model.ChatEvent, error) {
	started := time.Now()
	events, err := o.next.Stream(ctx, request)
	if err != nil {
		o.persist(ctx, Entry{RequestType: "chat", Model: o.name, LatencyMS: elapsedMS(started), Status: "failed", ErrorCode: errorCode(err)})
		return nil, err
	}
	observed := make(chan model.ChatEvent)
	go func() {
		defer close(observed)
		var callErr error
		var totals model.Usage
		for event := range events {
			if event.Usage != nil {
				totals = *event.Usage
			}
			if event.Err != nil {
				callErr = event.Err
			}
			select {
			case observed <- event:
			case <-ctx.Done():
				callErr = ctx.Err()
				o.recordChat(ctx, totals, started, callErr)
				return
			}
		}
		o.recordChat(ctx, totals, started, callErr)
	}()
	return observed, nil
}

func (o *ObservedChatModel) recordChat(ctx context.Context, totals model.Usage, started time.Time, callErr error) {
	o.persist(ctx, Entry{RequestType: "chat", Model: o.name, PromptTokens: totals.PromptTokens,
		CompletionTokens: totals.CompletionTokens, EstimatedCostUSD: o.pricing.Chat(totals.PromptTokens, totals.CompletionTokens),
		LatencyMS: elapsedMS(started), Status: status(callErr), ErrorCode: errorCode(callErr)})
}

func (o *ObservedEmbedder) persist(ctx context.Context, entry Entry) {
	persist(ctx, o.recorder, o.observer, entry)
}
func (o *ObservedReranker) persist(ctx context.Context, entry Entry) {
	persist(ctx, o.recorder, o.observer, entry)
}
func (o *ObservedChatModel) persist(ctx context.Context, entry Entry) {
	persist(ctx, o.recorder, o.observer, entry)
}

func persist(ctx context.Context, recorder Recorder, observer Observer, entry Entry) {
	meta := scopeFrom(ctx)
	entry.UserID, entry.KnowledgeBaseID = meta.userID, meta.knowledgeBaseID
	if observer != nil {
		observer.ObserveModel(entry)
	}
	if recorder == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = recorder.Record(writeCtx, entry)
}

func estimateTexts(texts []string) int {
	total := 0
	for _, text := range texts {
		total += max(1, len([]rune(text))/4)
	}
	return total
}

func elapsedMS(started time.Time) int {
	return max(0, int(math.Ceil(float64(time.Since(started).Microseconds())/1000)))
}
func status(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	return "PROVIDER_ERROR"
}
