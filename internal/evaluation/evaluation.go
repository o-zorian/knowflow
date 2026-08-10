package evaluation

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/internal/retrieval"
	"knowflow/internal/usage"
)

type Question struct {
	ID                  string   `json:"id"`
	KnowledgeBase       string   `json:"knowledge_base"`
	Question            string   `json:"question"`
	ExpectedDocumentIDs []string `json:"expected_document_ids"`
	ExpectedChunkIDs    []string `json:"expected_chunk_ids"`
	ReferenceAnswer     string   `json:"reference_answer"`
	Tags                []string `json:"tags"`
}

func LoadDataset(reader io.Reader) ([]Question, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	items := []Question{}
	ids := map[string]struct{}{}
	line := 0
	for scanner.Scan() {
		line++
		var item Question
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("dataset line %d: %w", line, err)
		}
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.KnowledgeBase) == "" || strings.TrimSpace(item.Question) == "" || strings.TrimSpace(item.ReferenceAnswer) == "" {
			return nil, fmt.Errorf("dataset line %d: required field is empty", line)
		}
		if len(item.ExpectedDocumentIDs) == 0 && len(item.ExpectedChunkIDs) == 0 {
			return nil, fmt.Errorf("dataset line %d: expected ids are empty", line)
		}
		if _, ok := ids[item.ID]; ok {
			return nil, fmt.Errorf("dataset line %d: duplicate id %s", line, item.ID)
		}
		ids[item.ID] = struct{}{}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(items) < 50 {
		return nil, fmt.Errorf("dataset has %d questions; at least 50 are required", len(items))
	}
	return items, nil
}

type ExperimentConfig struct {
	Name      string                        `json:"name"`
	Retrieval knowledgebase.RetrievalConfig `json:"retrieval"`
}
type Metrics struct {
	RecallAt1                 float64 `json:"recall_at_1"`
	RecallAt5                 float64 `json:"recall_at_5"`
	RecallAt10                float64 `json:"recall_at_10"`
	MRR                       float64 `json:"mrr"`
	CitationHitRate           float64 `json:"citation_hit_rate"`
	AverageRetrievalLatencyMS float64 `json:"average_retrieval_latency_ms"`
	P95RetrievalLatencyMS     float64 `json:"p95_retrieval_latency_ms"`
	AverageEndToEndLatencyMS  float64 `json:"average_end_to_end_latency_ms"`
	P95EndToEndLatencyMS      float64 `json:"p95_end_to_end_latency_ms"`
	AverageTokens             float64 `json:"average_tokens"`
	AverageEstimatedCostUSD   float64 `json:"average_estimated_cost_usd"`
}
type CaseResult struct {
	ID                 string   `json:"id"`
	Question           string   `json:"question"`
	FirstRelevantRank  int      `json:"first_relevant_rank"`
	CitationHit        bool     `json:"citation_hit"`
	RetrievalLatencyMS float64  `json:"retrieval_latency_ms"`
	EndToEndLatencyMS  float64  `json:"end_to_end_latency_ms"`
	Tokens             int      `json:"tokens"`
	EstimatedCostUSD   float64  `json:"estimated_cost_usd"`
	RetrievedChunkIDs  []string `json:"retrieved_chunk_ids"`
	Error              string   `json:"error,omitempty"`
}
type Experiment struct {
	Config   ExperimentConfig `json:"config"`
	Metrics  Metrics          `json:"metrics"`
	Cases    []CaseResult     `json:"cases"`
	Failures []CaseResult     `json:"failures"`
}
type Report struct {
	GeneratedAt   time.Time     `json:"generated_at"`
	Dataset       string        `json:"dataset"`
	QuestionCount int           `json:"question_count"`
	Pricing       usage.Pricing `json:"pricing"`
	Experiments   []Experiment  `json:"experiments"`
	Conclusions   []string      `json:"conclusions"`
}

type Runner struct {
	pool     *pgxpool.Pool
	embedder model.Embedder
	reranker model.Reranker
	chat     model.ChatModel
	pricing  usage.Pricing
}

func NewRunner(pool *pgxpool.Pool, embedder model.Embedder, reranker model.Reranker, chat model.ChatModel, pricing usage.Pricing) *Runner {
	return &Runner{pool: pool, embedder: embedder, reranker: reranker, chat: chat, pricing: pricing}
}

var evaluationNamespace = uuid.MustParse("798b0a22-71b1-4d62-a192-a8656fb4db75")

func stableID(value string) string { return uuid.NewSHA1(evaluationNamespace, []byte(value)).String() }

func (r *Runner) Seed(ctx context.Context, questions []Question) (string, string, error) {
	userID, kbID := stableID("eval-user"), stableID("eval-kb")
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,'evaluation@knowflow.local','offline-evaluation','admin') ON CONFLICT(id) DO UPDATE SET role='admin',status='active'`, userID); err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM knowledge_bases WHERE id=$1`, kbID); err != nil {
		return "", "", err
	}
	config, _ := json.Marshal(defaultExperimentConfig(20, 20, false).Retrieval)
	if _, err = tx.Exec(ctx, `INSERT INTO knowledge_bases(id,owner_id,name,description,embedding_model,embedding_dimension,retrieval_config) VALUES($1,$2,'demo-kb','M5 deterministic 60-question evaluation corpus','fake-embedding',1024,$3)`, kbID, userID, config); err != nil {
		return "", "", err
	}
	texts := make([]string, len(questions))
	for i, q := range questions {
		texts[i] = "Question: " + q.Question + "\nAnswer: " + q.ReferenceAnswer + "\nTags: " + strings.Join(q.Tags, " ")
	}
	vectors, err := r.embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return "", "", err
	}
	if len(vectors) != len(texts) {
		return "", "", errors.New("evaluation embedder returned invalid vector count")
	}
	for index, q := range questions {
		docID, chunkID := stableID(q.ExpectedDocumentIDs[0]), stableID(q.ExpectedChunkIDs[0])
		sum := sha256.Sum256([]byte(texts[index]))
		hash := hex.EncodeToString(sum[:])
		if _, err = tx.Exec(ctx, `INSERT INTO documents(id,knowledge_base_id,filename,mime_type,size_bytes,sha256,object_key,status,chunk_count,index_version) VALUES($1,$2,$3,'text/plain',$4,$5,$6,'ready',1,1)`, docID, kbID, q.ID+".txt", len(texts[index]), hash, "evaluation/"+q.ID); err != nil {
			return "", "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO document_chunks(id,knowledge_base_id,document_id,index_version,chunk_index,content,token_count,heading_path,content_hash,metadata,embedding) VALUES($1,$2,$3,1,0,$4,$5,$6,$7,$8,$9::vector)`, chunkID, kbID, docID, texts[index], max(1, len([]rune(texts[index]))/4), q.ID, hash, mapJSON(map[string]any{"evaluation_id": q.ID}), vectorLiteral(vectors[index])); err != nil {
			return "", "", err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return userID, kbID, nil
}

func (r *Runner) Run(ctx context.Context, datasetName string, questions []Question) (Report, error) {
	ownerID, kbID, err := r.Seed(ctx, questions)
	if err != nil {
		return Report{}, err
	}
	configs := []ExperimentConfig{defaultExperimentConfig(20, 0, false), defaultExperimentConfig(0, 20, false), defaultExperimentConfig(20, 20, false), defaultExperimentConfig(20, 20, true)}
	report := Report{GeneratedAt: time.Now().UTC(), Dataset: datasetName, QuestionCount: len(questions), Pricing: r.pricing}
	for _, cfg := range configs {
		payload, _ := json.Marshal(cfg.Retrieval)
		if _, err := r.pool.Exec(ctx, `UPDATE knowledge_bases SET retrieval_config=$2 WHERE id=$1`, kbID, payload); err != nil {
			return Report{}, err
		}
		service := retrieval.NewService(retrieval.NewPostgresStore(r.pool), r.embedder, r.reranker)
		experiment := Experiment{Config: cfg, Failures: []CaseResult{}}
		for _, question := range questions {
			experiment.Cases = append(experiment.Cases, r.runCase(ctx, service, ownerID, kbID, cfg, question))
		}
		experiment.Metrics = calculate(experiment.Cases)
		for _, item := range experiment.Cases {
			if item.Error != "" || item.FirstRelevantRank == 0 || item.FirstRelevantRank > 10 || !item.CitationHit {
				experiment.Failures = append(experiment.Failures, item)
			}
		}
		report.Experiments = append(report.Experiments, experiment)
	}
	report.Conclusions = conclusions(report.Experiments)
	return report, nil
}

func (r *Runner) runCase(ctx context.Context, service *retrieval.Service, ownerID, kbID string, cfg ExperimentConfig, q Question) CaseResult {
	started := time.Now()
	retrievalStarted := time.Now()
	response, err := service.Retrieve(ctx, ownerID, kbID, q.Question)
	retrievalLatency := float64(time.Since(retrievalStarted).Microseconds()) / 1000
	result := CaseResult{ID: q.ID, Question: q.Question, RetrievalLatencyMS: retrievalLatency}
	if err != nil {
		result.Error = err.Error()
		result.EndToEndLatencyMS = float64(time.Since(started).Microseconds()) / 1000
		return result
	}
	expectedChunks := map[string]struct{}{}
	for _, id := range q.ExpectedChunkIDs {
		expectedChunks[stableID(id)] = struct{}{}
	}
	expectedDocs := map[string]struct{}{}
	for _, id := range q.ExpectedDocumentIDs {
		expectedDocs[stableID(id)] = struct{}{}
	}
	for index, item := range response.Results {
		result.RetrievedChunkIDs = append(result.RetrievedChunkIDs, item.ChunkID)
		if result.FirstRelevantRank == 0 && relevant(item, expectedChunks, expectedDocs) {
			result.FirstRelevantRank = index + 1
		}
	}
	if len(response.Results) > 0 {
		result.CitationHit = relevant(response.Results[0], expectedChunks, expectedDocs)
		evidence := make([]model.ChatEvidence, len(response.Results))
		for i, item := range response.Results {
			evidence[i] = model.ChatEvidence{Number: i + 1, Content: item.Content}
		}
		events, streamErr := r.chat.Stream(ctx, model.ChatRequest{SystemPrompt: "Answer only from evidence and cite [n].", Messages: []model.ChatMessage{{Role: "user", Content: q.Question}}, Evidence: evidence})
		if streamErr != nil {
			result.Error = streamErr.Error()
		} else {
			var tokens model.Usage
			for event := range events {
				if event.Err != nil {
					result.Error = event.Err.Error()
				}
				if event.Usage != nil {
					tokens = *event.Usage
				}
			}
			result.Tokens = tokens.TotalTokens
			result.EstimatedCostUSD = r.pricing.Chat(tokens.PromptTokens, tokens.CompletionTokens)
		}
	}
	if cfg.Retrieval.DenseTopK > 0 {
		embeddingTokens := estimateTokens(q.Question)
		result.Tokens += embeddingTokens
		result.EstimatedCostUSD += r.pricing.Embedding(embeddingTokens)
	}
	if cfg.Retrieval.RerankEnabled {
		rerankTokens := estimateTokens(q.Question)
		for _, item := range response.Results {
			rerankTokens += estimateTokens(item.Content)
		}
		result.Tokens += rerankTokens
		result.EstimatedCostUSD += r.pricing.Rerank(rerankTokens)
	}
	result.EndToEndLatencyMS = float64(time.Since(started).Microseconds()) / 1000
	return result
}

func defaultExperimentConfig(dense, sparse int, rerank bool) ExperimentConfig {
	name := "Dense only"
	if dense == 0 {
		name = "Sparse only"
	} else if sparse > 0 {
		name = "Dense + Sparse + RRF"
		if rerank {
			name += " + Reranker"
		}
	}
	return ExperimentConfig{Name: name, Retrieval: knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: dense, SparseTopK: sparse, RerankTopK: 10, FinalTopK: 10, MinimumScore: 0, RRFK: 60, RerankEnabled: rerank}}
}
func relevant(item retrieval.Result, chunks, docs map[string]struct{}) bool {
	_, chunk := chunks[item.ChunkID]
	_, doc := docs[item.DocumentID]
	return chunk || doc
}
func calculate(cases []CaseResult) Metrics {
	if len(cases) == 0 {
		return Metrics{}
	}
	var m Metrics
	retrievals := make([]float64, 0, len(cases))
	ends := make([]float64, 0, len(cases))
	for _, item := range cases {
		if item.FirstRelevantRank == 1 {
			m.RecallAt1++
		}
		if item.FirstRelevantRank > 0 && item.FirstRelevantRank <= 5 {
			m.RecallAt5++
		}
		if item.FirstRelevantRank > 0 && item.FirstRelevantRank <= 10 {
			m.RecallAt10++
		}
		if item.FirstRelevantRank > 0 {
			m.MRR += 1 / float64(item.FirstRelevantRank)
		}
		if item.CitationHit {
			m.CitationHitRate++
		}
		m.AverageRetrievalLatencyMS += item.RetrievalLatencyMS
		m.AverageEndToEndLatencyMS += item.EndToEndLatencyMS
		m.AverageTokens += float64(item.Tokens)
		m.AverageEstimatedCostUSD += item.EstimatedCostUSD
		retrievals = append(retrievals, item.RetrievalLatencyMS)
		ends = append(ends, item.EndToEndLatencyMS)
	}
	n := float64(len(cases))
	m.RecallAt1 /= n
	m.RecallAt5 /= n
	m.RecallAt10 /= n
	m.MRR /= n
	m.CitationHitRate /= n
	m.AverageRetrievalLatencyMS /= n
	m.AverageEndToEndLatencyMS /= n
	m.AverageTokens /= n
	m.AverageEstimatedCostUSD /= n
	m.P95RetrievalLatencyMS = percentile(retrievals, .95)
	m.P95EndToEndLatencyMS = percentile(ends, .95)
	return m
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Ceil(p*float64(len(copyValues)))) - 1
	return copyValues[max(0, min(index, len(copyValues)-1))]
}
func conclusions(experiments []Experiment) []string {
	if len(experiments) == 0 {
		return nil
	}
	best := experiments[0]
	for _, e := range experiments[1:] {
		if e.Metrics.RecallAt10 > best.Metrics.RecallAt10 || (e.Metrics.RecallAt10 == best.Metrics.RecallAt10 && e.Metrics.MRR > best.Metrics.MRR) {
			best = e
		}
	}
	result := []string{fmt.Sprintf("Highest Recall@10/MRR combination: %s (%.4f / %.4f).", best.Config.Name, best.Metrics.RecallAt10, best.Metrics.MRR)}
	dense, sparse := experiments[0], experiments[1]
	if dense.Metrics.RecallAt10 > sparse.Metrics.RecallAt10 {
		result = append(result, "Dense retrieval has higher Recall@10 than sparse retrieval on this deterministic lexical corpus.")
	} else if sparse.Metrics.RecallAt10 > dense.Metrics.RecallAt10 {
		result = append(result, "Sparse retrieval has higher Recall@10 than dense retrieval; improve embeddings or add domain examples.")
	} else {
		result = append(result, "Dense and sparse Recall@10 are tied; inspect MRR and failure cases before choosing a default.")
	}
	if len(experiments) >= 4 && experiments[3].Metrics.MRR < experiments[2].Metrics.MRR {
		result = append(result, "Reranking reduced MRR on this dataset; tune or replace the reranker before enabling it by default.")
	} else {
		result = append(result, "Reranking did not reduce MRR relative to RRF on this run.")
	}
	return result
}

func WriteReports(report Report, jsonPath, markdownPath string) error {
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, append(payload, '\n'), 0644); err != nil {
		return err
	}
	return os.WriteFile(markdownPath, []byte(Markdown(report)), 0644)
}
func Markdown(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# KnowFlow M5 Retrieval Evaluation\n\nGenerated: `%s`  \nDataset: `%s`  \nQuestions: **%d**\n\n", report.GeneratedAt.Format(time.RFC3339), report.Dataset, report.QuestionCount)
	fmt.Fprintf(&b, "Configured illustrative pricing (USD / 1M tokens): chat input %.4f, chat output %.4f, embedding %.4f, rerank input %.4f.\n\n", report.Pricing.ChatInputPerMillion, report.Pricing.ChatOutputPerMillion, report.Pricing.EmbeddingPerMillion, report.Pricing.RerankInputPerMillion)
	b.WriteString("## Experiment configurations\n\n| Configuration | Dense K | Sparse K | RRF K | Rerank | Rerank K | Final K |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range report.Experiments {
		c := e.Config.Retrieval
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %t | %d | %d |\n", e.Config.Name, c.DenseTopK, c.SparseTopK, c.RRFK, c.RerankEnabled, c.RerankTopK, c.FinalTopK)
	}
	b.WriteString("\n## Metrics\n\n| Configuration | Recall@1 | Recall@5 | Recall@10 | MRR | Citation hit | Retrieval avg / P95 ms | E2E avg / P95 ms | Avg tokens | Avg cost USD |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range report.Experiments {
		m := e.Metrics
		fmt.Fprintf(&b, "| %s | %.4f | %.4f | %.4f | %.4f | %.4f | %.3f / %.3f | %.3f / %.3f | %.2f | %.8f |\n", e.Config.Name, m.RecallAt1, m.RecallAt5, m.RecallAt10, m.MRR, m.CitationHitRate, m.AverageRetrievalLatencyMS, m.P95RetrievalLatencyMS, m.AverageEndToEndLatencyMS, m.P95EndToEndLatencyMS, m.AverageTokens, m.AverageEstimatedCostUSD)
	}
	b.WriteString("\n## Failure cases\n")
	for _, e := range report.Experiments {
		fmt.Fprintf(&b, "\n### %s\n\n", e.Config.Name)
		if len(e.Failures) == 0 {
			b.WriteString("No Recall@10 or citation failures.\n")
			continue
		}
		b.WriteString("| ID | First relevant rank | Citation hit | Error |\n|---|---:|---:|---|\n")
		for i, item := range e.Failures {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "| %s | %d | %t | %s |\n", item.ID, item.FirstRelevantRank, item.CitationHit, strings.ReplaceAll(item.Error, "|", "\\|"))
		}
	}
	b.WriteString("\n## Conclusions and improvements\n\n")
	for _, item := range report.Conclusions {
		fmt.Fprintf(&b, "- %s\n", item)
	}
	b.WriteString("- Review the JSON per-question cases for rank regressions; expand hard negatives and multi-hop questions before using the numbers as a production quality claim.\n- Latency and cost use the local fake models and configured price table; repeat with explicitly enabled production providers for provider-specific capacity planning.\n")
	return b.String()
}

func vectorLiteral(vector []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vector {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", v)
	}
	b.WriteByte(']')
	return b.String()
}
func mapJSON(value any) []byte        { payload, _ := json.Marshal(value); return payload }
func estimateTokens(value string) int { return max(1, len([]rune(value))/4) }
