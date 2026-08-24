package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/internal/usage"
)

const reportTitle = "KnowFlow real-world-v1 真实文档评测"

type options struct {
	phase, baseURL, envFile, dataset, corpus, state, jsonReport, markdownReport, forceStrategies string
	timeout                                                                                      time.Duration
}

type question struct {
	ID               string   `json:"id"`
	Question         string   `json:"question"`
	ReferenceAnswer  string   `json:"reference_answer"`
	ExpectedSources  []string `json:"expected_sources"`
	ExpectedEvidence []string `json:"expected_evidence"`
	Tags             []string `json:"tags"`
	Conversation     string   `json:"conversation_group"`
	Turn             int      `json:"turn"`
	Unanswerable     bool     `json:"unanswerable"`
}

type runState struct {
	Email           string                   `json:"email"`
	Password        string                   `json:"password"`
	KnowledgeBaseID string                   `json:"knowledge_base_id"`
	StartedAt       time.Time                `json:"started_at"`
	Documents       map[string]documentState `json:"documents"`
}

type documentState struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Format     string `json:"format"`
	Status     string `json:"status"`
	ChunkCount int    `json:"chunk_count"`
}

type providerInfo struct {
	LLMBaseURL       string `json:"llm_base_url,omitempty"`
	LLMModel         string `json:"llm_model,omitempty"`
	EmbeddingBaseURL string `json:"embedding_base_url,omitempty"`
	EmbeddingModel   string `json:"embedding_model,omitempty"`
	RerankProvider   string `json:"rerank_provider,omitempty"`
	RerankModel      string `json:"rerank_model,omitempty"`
	VikingDBHost     string `json:"vikingdb_host,omitempty"`
	VikingRegion     string `json:"vikingdb_region,omitempty"`
	DeepSeekReal     bool   `json:"deepseek_real"`
	EmbeddingReal    bool   `json:"embedding_real"`
	RerankerReal     bool   `json:"reranker_real"`
}

type pipelineValidation struct {
	PublicUploadAPI bool                   `json:"public_upload_api"`
	MinIOObjects    int                    `json:"minio_objects"`
	RedisQueueEmpty bool                   `json:"redis_queue_empty"`
	WorkerSucceeded int                    `json:"worker_succeeded"`
	ReadyDocuments  int                    `json:"ready_documents"`
	ChunkCount      int                    `json:"chunk_count"`
	Vector1024Count int                    `json:"vector_1024_count"`
	ProviderUsage   []providerUsageSummary `json:"provider_usage"`
}

type providerUsageSummary struct {
	RequestType      string  `json:"request_type"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TextCount        int     `json:"text_count"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	Failed           int     `json:"failed"`
}

type retrievalItem struct {
	Number     int     `json:"number"`
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Score      float64 `json:"score"`
}

type retrievalTrace struct {
	Strategy          string          `json:"strategy"`
	LatencyMS         float64         `json:"latency_ms"`
	Results           []retrievalItem `json:"results"`
	RerankAttempted   bool            `json:"rerank_attempted"`
	RerankFallback    bool            `json:"rerank_fallback"`
	RewriteApplied    bool            `json:"rewrite_applied"`
	RewriteFallback   bool            `json:"rewrite_fallback"`
	DenseResultCount  int             `json:"dense_result_count"`
	SparseResultCount int             `json:"sparse_result_count"`
}

type citation struct {
	Number      int     `json:"number"`
	ChunkIndex  int     `json:"chunk_index"`
	DocumentID  string  `json:"document_id"`
	Filename    string  `json:"filename"`
	ChunkID     string  `json:"chunk_id"`
	Excerpt     string  `json:"excerpt"`
	HeadingPath string  `json:"heading_path,omitempty"`
	Location    string  `json:"location,omitempty"`
	Score       float64 `json:"score"`
}

type completedMessage struct {
	Model            string     `json:"model"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
	TotalTokens      int        `json:"total_tokens"`
	EstimatedCostUSD float64    `json:"estimated_cost_usd"`
	LatencyMS        int        `json:"latency_ms"`
	Citations        []citation `json:"citations"`
}

type streamResult struct {
	Answer     string
	Trace      retrievalTrace
	Citations  []citation
	Usage      model.Usage
	Completed  completedMessage
	EventNames map[string]bool
}

type judgeResult struct {
	AnswerCorrect     bool   `json:"answer_correct"`
	EvidenceSupported bool   `json:"evidence_supported"`
	CorrectRefusal    bool   `json:"correct_refusal"`
	Hallucination     bool   `json:"hallucination"`
	Rationale         string `json:"rationale"`
}

type caseResult struct {
	ID                   string         `json:"id"`
	Question             string         `json:"question"`
	ReferenceAnswer      string         `json:"reference_answer"`
	Answer               string         `json:"answer"`
	Error                string         `json:"error,omitempty"`
	Tags                 []string       `json:"tags"`
	ExpectedSources      []string       `json:"expected_sources"`
	ExpectedEvidence     []string       `json:"expected_evidence"`
	FirstRelevantRank    int            `json:"first_relevant_rank"`
	CitationHit          bool           `json:"citation_hit"`
	RetrievedDocumentIDs []string       `json:"retrieved_document_ids"`
	Citations            []citation     `json:"citations"`
	Retrieval            retrievalTrace `json:"retrieval"`
	Judge                judgeResult    `json:"judge"`
	AnswerTokens         int            `json:"answer_tokens"`
	JudgeTokens          int            `json:"judge_tokens"`
	TotalTokens          int            `json:"total_tokens"`
	AnswerCostUSD        float64        `json:"answer_cost_usd"`
	JudgeCostUSD         float64        `json:"judge_cost_usd"`
	TotalCostUSD         float64        `json:"total_cost_usd"`
	EndToEndLatencyMS    float64        `json:"end_to_end_latency_ms"`
}

type metrics struct {
	RecallAt1                 float64 `json:"recall_at_1"`
	RecallAt5                 float64 `json:"recall_at_5"`
	RecallAt10                float64 `json:"recall_at_10"`
	MRR                       float64 `json:"mrr"`
	CitationHitRate           float64 `json:"citation_hit_rate"`
	AnswerAccuracy            float64 `json:"answer_accuracy"`
	EvidenceSupportRate       float64 `json:"evidence_support_rate"`
	CorrectRefusalRate        float64 `json:"correct_refusal_rate"`
	HallucinationRate         float64 `json:"hallucination_rate"`
	AverageRetrievalLatencyMS float64 `json:"average_retrieval_latency_ms"`
	P95RetrievalLatencyMS     float64 `json:"p95_retrieval_latency_ms"`
	AverageEndToEndLatencyMS  float64 `json:"average_end_to_end_latency_ms"`
	P95EndToEndLatencyMS      float64 `json:"p95_end_to_end_latency_ms"`
	AverageTokens             float64 `json:"average_tokens"`
	TotalTokens               float64 `json:"total_tokens"`
	AverageCostUSD            float64 `json:"average_cost_usd"`
	TotalCostUSD              float64 `json:"total_cost_usd"`
}

type formatMetric struct {
	Format              string   `json:"format"`
	Cases               int      `json:"cases"`
	Succeeded           int      `json:"succeeded"`
	SuccessRate         float64  `json:"success_rate"`
	AnswerAccuracy      float64  `json:"answer_accuracy"`
	EvidenceSupportRate float64  `json:"evidence_support_rate"`
	CorrectRefusalRate  float64  `json:"correct_refusal_rate"`
	HallucinationRate   float64  `json:"hallucination_rate"`
	FailureCases        []string `json:"failure_cases"`
}

type strategy struct {
	Name     string                        `json:"name"`
	Config   knowledgebase.RetrievalConfig `json:"retrieval_config"`
	FullReal bool                          `json:"full_real"`
	Metrics  metrics                       `json:"metrics"`
	Formats  []formatMetric                `json:"format_metrics"`
	Cases    []caseResult                  `json:"cases"`
}

type report struct {
	Title                  string             `json:"title"`
	EvaluationType         string             `json:"evaluation_type"`
	Dataset                string             `json:"dataset"`
	RegressionBaselineNote string             `json:"regression_baseline_note"`
	GeneratedAt            time.Time          `json:"generated_at"`
	StartedAt              time.Time          `json:"started_at"`
	QuestionCount          int                `json:"question_count"`
	DocumentCount          int                `json:"document_count"`
	Providers              providerInfo       `json:"providers"`
	Pricing                usage.Pricing      `json:"pricing"`
	Pipeline               pipelineValidation `json:"pipeline"`
	Experiments            []strategy         `json:"experiments"`
	Conclusions            []string           `json:"conclusions"`
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiClient struct {
	baseURL, token string
	http           *http.Client
}

func main() {
	var cfg options
	flag.StringVar(&cfg.phase, "phase", "all", "provision, evaluate, or all")
	flag.StringVar(&cfg.baseURL, "base-url", "http://127.0.0.1:8080/api/v1", "public KnowFlow API base URL")
	flag.StringVar(&cfg.envFile, "env-file", ".env", "provider and local service environment file")
	flag.StringVar(&cfg.dataset, "dataset", "eval/real-world-v1/real-world-v1.jsonl", "real-world JSONL dataset")
	flag.StringVar(&cfg.corpus, "corpus", "eval/real-world-v1/corpus", "real document corpus directory")
	flag.StringVar(&cfg.state, "state", "eval/real-world-v1/run-state.json", "resumable provision state")
	flag.StringVar(&cfg.jsonReport, "json", "eval/results/real-world-evaluation.json", "independent JSON report")
	flag.StringVar(&cfg.markdownReport, "markdown", "eval/results/real-world-evaluation.md", "independent Markdown report")
	flag.StringVar(&cfg.forceStrategies, "force-strategies", "", "comma-separated strategy names to rerun instead of resuming")
	flag.DurationVar(&cfg.timeout, "timeout", 2*time.Hour, "overall phase timeout")
	flag.Parse()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "real-world evaluation failed:", err)
		os.Exit(1)
	}
}

func run(cfg options) error {
	if cfg.phase != "provision" && cfg.phase != "evaluate" && cfg.phase != "all" {
		return errors.New("phase must be provision, evaluate, or all")
	}
	if err := loadEnv(cfg.envFile); err != nil {
		return err
	}
	questions, err := loadQuestions(cfg.dataset)
	if err != nil {
		return err
	}
	providers, pricing, err := validateProviders(cfg.phase != "provision")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	client := &apiClient{baseURL: strings.TrimRight(cfg.baseURL, "/"), http: &http.Client{Timeout: 5 * time.Minute}}
	if err := client.health(ctx); err != nil {
		return err
	}
	var state runState
	if cfg.phase == "provision" || cfg.phase == "all" {
		state, err = provision(ctx, client, cfg, questions, providers)
		if err != nil {
			return err
		}
	}
	if cfg.phase == "evaluate" {
		if err = readJSON(cfg.state, &state); err != nil {
			return err
		}
	}
	if cfg.phase == "evaluate" || cfg.phase == "all" {
		return evaluate(ctx, client, cfg, questions, state, providers, pricing)
	}
	return nil
}

func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		value := strings.TrimSpace(parts[1])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if err := os.Setenv(strings.TrimSpace(parts[0]), value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func loadQuestions(path string) ([]question, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	items := []question{}
	ids := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var item question
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		if item.ID == "" || item.Question == "" || item.ReferenceAnswer == "" || ids[item.ID] {
			return nil, fmt.Errorf("invalid or duplicate question %q", item.ID)
		}
		if !item.Unanswerable && (len(item.ExpectedSources) == 0 || len(item.ExpectedEvidence) == 0) {
			return nil, fmt.Errorf("question %s is missing source/evidence", item.ID)
		}
		ids[item.ID] = true
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(items) < 60 {
		return nil, fmt.Errorf("real-world dataset has %d questions; at least 60 required", len(items))
	}
	return items, nil
}

func validateProviders(requireReranker bool) (providerInfo, usage.Pricing, error) {
	info := providerInfo{
		LLMBaseURL: os.Getenv("LLM_BASE_URL"), LLMModel: os.Getenv("LLM_MODEL"),
		EmbeddingBaseURL: os.Getenv("EMBEDDING_BASE_URL"), EmbeddingModel: os.Getenv("EMBEDDING_MODEL"),
		RerankProvider: os.Getenv("RERANK_PROVIDER"), RerankModel: os.Getenv("RERANK_MODEL"),
		VikingDBHost: os.Getenv("VIKINGDB_HOST"), VikingRegion: os.Getenv("VIKINGDB_REGION"),
	}
	info.DeepSeekReal = strings.Contains(strings.ToLower(info.LLMBaseURL), "deepseek") && strings.Contains(strings.ToLower(info.LLMModel), "deepseek") && os.Getenv("LLM_API_KEY") != ""
	info.EmbeddingReal = info.EmbeddingBaseURL != "" && info.EmbeddingModel != "" && os.Getenv("EMBEDDING_API_KEY") != "" && !strings.Contains(strings.ToLower(info.EmbeddingModel), "fake")
	if !info.DeepSeekReal {
		return info, usage.Pricing{}, errors.New("real DeepSeek LLM is required; Fake/fallback providers are forbidden")
	}
	if !info.EmbeddingReal {
		return info, usage.Pricing{}, errors.New("real embedding provider is required; Fake/fallback embeddings are forbidden")
	}
	if requireReranker {
		if info.RerankProvider != "vikingdb" || info.RerankModel == "" || info.VikingDBHost == "" || info.VikingRegion == "" || os.Getenv("VIKINGDB_AK") == "" || os.Getenv("VIKINGDB_SK") == "" {
			return info, usage.Pricing{}, errors.New("complete VikingDB AK/SK reranker configuration is required")
		}
		client, err := model.NewVikingDBRerankClient(info.VikingDBHost, info.VikingRegion, os.Getenv("VIKINGDB_AK"), os.Getenv("VIKINGDB_SK"), info.RerankModel, nil)
		if err != nil {
			return info, usage.Pricing{}, err
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := client.Rerank(probeCtx, "哪段描述混合检索", []model.RerankDocument{{Content: "混合检索融合稠密与稀疏结果。"}, {Content: "办公室停车说明。"}}, 2)
		if err != nil || len(result) != 2 {
			return info, usage.Pricing{}, fmt.Errorf("real VikingDB reranker probe failed: %w", err)
		}
		info.RerankerReal = true
	}
	pricing := usage.Pricing{
		ChatInputPerMillion: parseFloatEnv("EVAL_CHAT_INPUT_COST_PER_MILLION_USD"), ChatOutputPerMillion: parseFloatEnv("EVAL_CHAT_OUTPUT_COST_PER_MILLION_USD"),
		EmbeddingPerMillion: parseFloatEnv("EVAL_EMBEDDING_COST_PER_MILLION_USD"), RerankInputPerMillion: parseFloatEnv("EVAL_RERANK_COST_PER_MILLION_USD"),
	}
	return info, pricing, nil
}

func parseFloatEnv(name string) float64 {
	value, _ := strconv.ParseFloat(os.Getenv(name), 64)
	return value
}

func provision(ctx context.Context, client *apiClient, cfg options, questions []question, providers providerInfo) (runState, error) {
	now := time.Now().UTC()
	state := runState{Email: fmt.Sprintf("real-world-v1-%d@knowflow.local", now.UnixNano()), Password: "real-world-v1-strong-password", StartedAt: now, Documents: map[string]documentState{}}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := client.json(ctx, http.MethodPost, "/auth/register", map[string]string{"email": state.Email, "password": state.Password}, &tokens); err != nil {
		return state, fmt.Errorf("register evaluator user: %w", err)
	}
	client.token = tokens.AccessToken
	config := knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 20, SparseTopK: 20, RerankTopK: 10, FinalTopK: 10, MinimumScore: 0, RRFK: 60}
	var kb struct {
		ID string `json:"id"`
	}
	if err := client.json(ctx, http.MethodPost, "/knowledge-bases", map[string]any{"name": "real-world-v1 " + now.Format(time.RFC3339), "description": "真实格式/真实 Provider 独立文档质量评测；不得与 M5 回归基线混称。", "embedding_model": providers.EmbeddingModel, "retrieval_config": config}, &kb); err != nil {
		return state, fmt.Errorf("create real-world knowledge base: %w", err)
	}
	state.KnowledgeBaseID = kb.ID
	files, err := os.ReadDir(cfg.corpus)
	if err != nil {
		return state, err
	}
	if len(files) < 8 || len(files) > 12 {
		return state, fmt.Errorf("corpus contains %d files; expected 8-12", len(files))
	}
	for _, entry := range files {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(cfg.corpus, entry.Name())
		doc, err := client.upload(ctx, kb.ID, path)
		if err != nil {
			return state, fmt.Errorf("upload %s: %w", entry.Name(), err)
		}
		state.Documents[entry.Name()] = doc
		if err := writeJSON(cfg.state, state); err != nil {
			return state, err
		}
		fmt.Printf("UPLOADED %s id=%s\n", entry.Name(), doc.ID)
	}
	deadline := time.NewTicker(2 * time.Second)
	defer deadline.Stop()
	for {
		ready := 0
		for filename, current := range state.Documents {
			var doc documentState
			if err := client.json(ctx, http.MethodGet, "/documents/"+current.ID, nil, &doc); err != nil {
				return state, err
			}
			doc.Filename, doc.Format = filename, strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
			state.Documents[filename] = doc
			if doc.Status == "failed" {
				return state, fmt.Errorf("document %s indexing failed", filename)
			}
			if doc.Status == "ready" {
				ready++
			}
		}
		_ = writeJSON(cfg.state, state)
		fmt.Printf("INDEXING ready=%d/%d\n", ready, len(state.Documents))
		if ready == len(state.Documents) {
			break
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-deadline.C:
		}
	}
	if err := validateEvidenceViaAPI(ctx, client, state, questions); err != nil {
		return state, err
	}
	pipeline, err := validatePipeline(ctx, state)
	if err != nil {
		return state, err
	}
	if !pipeline.PublicUploadAPI || pipeline.MinIOObjects != len(state.Documents) || !pipeline.RedisQueueEmpty || pipeline.WorkerSucceeded != len(state.Documents) || pipeline.ReadyDocuments != len(state.Documents) || pipeline.ChunkCount == 0 || pipeline.Vector1024Count != pipeline.ChunkCount {
		return state, fmt.Errorf("pipeline validation incomplete: %+v", pipeline)
	}
	fmt.Printf("PROVISIONED kb=%s documents=%d chunks=%d minio=%d vectors=%d\n", state.KnowledgeBaseID, len(state.Documents), pipeline.ChunkCount, pipeline.MinIOObjects, pipeline.Vector1024Count)
	return state, nil
}

func validateEvidenceViaAPI(ctx context.Context, client *apiClient, state runState, questions []question) error {
	texts := map[string]string{}
	for filename, doc := range state.Documents {
		var page struct {
			Items []struct {
				Content string `json:"content"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err := client.json(ctx, http.MethodGet, "/documents/"+doc.ID+"/chunks?page=1&page_size=100", nil, &page); err != nil {
			return err
		}
		var combined strings.Builder
		for _, chunk := range page.Items {
			combined.WriteString(normalize(chunk.Content))
		}
		texts[filename] = combined.String()
	}
	for _, item := range questions {
		for _, expected := range item.ExpectedEvidence {
			found := false
			for _, source := range item.ExpectedSources {
				if strings.Contains(texts[source], normalize(expected)) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("expected evidence for %s not found in public chunk API", item.ID)
			}
		}
	}
	return nil
}

func evaluate(ctx context.Context, client *apiClient, cfg options, questions []question, state runState, providers providerInfo, pricing usage.Pricing) error {
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := client.json(ctx, http.MethodPost, "/auth/login", map[string]string{"email": state.Email, "password": state.Password}, &tokens); err != nil {
		return fmt.Errorf("login provisioned evaluator: %w", err)
	}
	client.token = tokens.AccessToken
	judge, err := model.NewOpenAIClient(providers.LLMBaseURL, os.Getenv("LLM_API_KEY"), providers.LLMModel, nil)
	if err != nil {
		return err
	}
	judge.SetMaxRetries(3)
	pipeline, err := validatePipeline(ctx, state)
	if err != nil {
		return err
	}
	rep := report{Title: reportTitle, EvaluationType: "real-world-document-evaluation", Dataset: cfg.dataset, RegressionBaselineNote: "eval/datasets/knowflow-m5.jsonl 的 60 题仅是确定性回归基线；其接近满分结果不得称为真实文档质量评测。", GeneratedAt: time.Now().UTC(), StartedAt: state.StartedAt, QuestionCount: len(questions), DocumentCount: len(state.Documents), Providers: providers, Pricing: pricing, Pipeline: pipeline}
	configs := []strategy{
		{Name: "Dense", FullReal: true, Config: knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 20, SparseTopK: 0, RerankTopK: 10, FinalTopK: 10, RRFK: 60}},
		{Name: "Sparse", FullReal: true, Config: knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 0, SparseTopK: 20, RerankTopK: 10, FinalTopK: 10, RRFK: 60}},
		{Name: "Dense+Sparse+RRF", FullReal: true, Config: knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 20, SparseTopK: 20, RerankTopK: 10, FinalTopK: 10, RRFK: 60}},
		{Name: "Dense+Sparse+RRF+Reranker", FullReal: providers.RerankerReal, Config: knowledgebase.RetrievalConfig{ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 20, SparseTopK: 20, RerankTopK: 10, FinalTopK: 10, RRFK: 60, RerankEnabled: true}},
	}
	forced := map[string]bool{}
	for _, name := range strings.Split(cfg.forceStrategies, ",") {
		if name = strings.TrimSpace(name); name != "" {
			forced[name] = true
		}
	}
	for name := range forced {
		known := false
		for _, configured := range configs {
			if configured.Name == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown forced strategy %q", name)
		}
	}
	var checkpoint report
	if err := readJSON(cfg.jsonReport, &checkpoint); err == nil && checkpoint.StartedAt.Equal(state.StartedAt) {
		for index := range configs {
			if !forced[configs[index].Name] && index < len(checkpoint.Experiments) && checkpoint.Experiments[index].Name == configs[index].Name && len(checkpoint.Experiments[index].Cases) == len(questions) {
				configs[index] = checkpoint.Experiments[index]
				fmt.Printf("RESUME strategy=%s cases=%d/%d\n", configs[index].Name, len(configs[index].Cases), len(questions))
			}
		}
	}
	for strategyIndex := range configs {
		current := &configs[strategyIndex]
		if len(current.Cases) == len(questions) {
			rep.Experiments = append([]strategy(nil), configs[:strategyIndex+1]...)
			continue
		}
		if err := client.json(ctx, http.MethodPatch, "/knowledge-bases/"+state.KnowledgeBaseID, map[string]any{"retrieval_config": current.Config}, nil); err != nil {
			return err
		}
		conversations := map[string]string{}
		for index, item := range questions {
			conversationID := conversations[item.Conversation]
			if conversationID == "" {
				var created struct {
					ID string `json:"id"`
				}
				if err := client.json(ctx, http.MethodPost, "/conversations", map[string]string{"knowledge_base_id": state.KnowledgeBaseID, "title": current.Name + " / " + item.Conversation}, &created); err != nil {
					return err
				}
				conversationID = created.ID
				conversations[item.Conversation] = conversationID
			}
			caseStarted := time.Now()
			streamed, streamErr := streamWithRetry(ctx, client, conversationID, item.Question, current.Config.RerankEnabled)
			result := buildCase(item, state, streamed, pricing, time.Since(caseStarted), streamErr)
			if streamErr == nil {
				judged, judgeUsage, judgeErr := judgeAnswer(ctx, judge, item, streamed)
				result.Judge = judged
				result.JudgeTokens = judgeUsage.TotalTokens
				result.JudgeCostUSD = pricing.Chat(judgeUsage.PromptTokens, judgeUsage.CompletionTokens)
				result.TotalTokens = result.AnswerTokens + result.JudgeTokens
				result.TotalCostUSD = result.AnswerCostUSD + result.JudgeCostUSD
				if judgeErr != nil {
					result.Error = judgeErr.Error()
				}
			}
			if current.Config.RerankEnabled && (streamErr != nil || !streamed.Trace.RerankAttempted || streamed.Trace.RerankFallback || !strings.Contains(streamed.Trace.Strategy, "+rerank")) {
				current.FullReal = false
				return fmt.Errorf("real reranker was not successfully applied for %s: trace=%+v error=%v", item.ID, streamed.Trace, streamErr)
			}
			current.Cases = append(current.Cases, result)
			current.Metrics = calculateMetrics(current.Cases, questions)
			current.Formats = calculateFormats(current.Cases, questions)
			rep.Experiments = append([]strategy(nil), configs[:strategyIndex+1]...)
			rep.GeneratedAt = time.Now().UTC()
			_ = writeReports(rep, cfg.jsonReport, cfg.markdownReport)
			fmt.Printf("EVAL strategy=%s case=%d/%d id=%s correct=%t support=%t refusal=%t hallucination=%t\n", current.Name, index+1, len(questions), item.ID, result.Judge.AnswerCorrect, result.Judge.EvidenceSupported, result.Judge.CorrectRefusal, result.Judge.Hallucination)
		}
	}
	rep.Experiments = configs
	rep.Pipeline, err = validatePipeline(ctx, state)
	if err != nil {
		return err
	}
	rep.GeneratedAt = time.Now().UTC()
	rep.Conclusions = conclusions(rep)
	return writeReports(rep, cfg.jsonReport, cfg.markdownReport)
}

func streamWithRetry(ctx context.Context, client *apiClient, conversationID, question string, requireReranker bool) (streamResult, error) {
	var streamed streamResult
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		streamed, err = client.stream(ctx, conversationID, question)
		rerankApplied := !requireReranker || (err == nil && streamed.Trace.RerankAttempted && !streamed.Trace.RerankFallback && strings.Contains(streamed.Trace.Strategy, "+rerank"))
		if err == nil && rerankApplied {
			return streamed, nil
		}
		if err == nil && !rerankApplied {
			err = fmt.Errorf("real reranker was not applied: trace=%+v", streamed.Trace)
		}
		if attempt == 3 {
			break
		}
		fmt.Printf("RETRY conversation=%s attempt=%d/3 error=%v\n", conversationID, attempt+1, err)
		timer := time.NewTimer(time.Duration(1<<(attempt-1)) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return streamed, ctx.Err()
		case <-timer.C:
		}
	}
	return streamed, err
}

func buildCase(q question, state runState, streamed streamResult, pricing usage.Pricing, elapsed time.Duration, streamErr error) caseResult {
	result := caseResult{ID: q.ID, Question: q.Question, ReferenceAnswer: q.ReferenceAnswer, ExpectedSources: q.ExpectedSources, ExpectedEvidence: q.ExpectedEvidence, Tags: q.Tags, Answer: streamed.Answer, Citations: streamed.Citations, Retrieval: streamed.Trace, EndToEndLatencyMS: float64(elapsed.Milliseconds())}
	if streamErr != nil {
		result.Error = streamErr.Error()
		return result
	}
	expectedIDs := map[string]bool{}
	for _, source := range q.ExpectedSources {
		expectedIDs[state.Documents[source].ID] = true
	}
	for index, item := range streamed.Trace.Results {
		result.RetrievedDocumentIDs = append(result.RetrievedDocumentIDs, item.DocumentID)
		if result.FirstRelevantRank == 0 && expectedIDs[item.DocumentID] {
			result.FirstRelevantRank = index + 1
		}
	}
	for _, item := range streamed.Citations {
		for _, source := range q.ExpectedSources {
			if item.Filename == source {
				result.CitationHit = true
			}
		}
	}
	result.AnswerTokens = streamed.Completed.TotalTokens
	if result.AnswerTokens == 0 {
		result.AnswerTokens = streamed.Usage.TotalTokens
	}
	result.AnswerCostUSD = pricing.Chat(streamed.Completed.PromptTokens, streamed.Completed.CompletionTokens)
	return result
}

func judgeAnswer(ctx context.Context, judge model.ChatModel, q question, streamed streamResult) (judgeResult, model.Usage, error) {
	var evidence strings.Builder
	for _, item := range streamed.Citations {
		fmt.Fprintf(&evidence, "[%d] file=%s\n%s\n", item.Number, item.Filename, item.Excerpt)
	}
	prompt := fmt.Sprintf(`请严格评估一个 RAG 答案。只输出一个 JSON 对象，不要 Markdown，不要额外文字：
{"answer_correct":true|false,"evidence_supported":true|false,"correct_refusal":true|false,"hallucination":true|false,"rationale":"不超过80字"}

规则：
1. answer_correct：对可回答题，答案与参考答案在关键事实和数值上等价；不可回答题则表示是否正确说明资料不足。
2. evidence_supported：答案中的关键事实是否全部受到“实际引用证据”支持；没有实际引用时必须为 false（不可回答题正确拒答时可为 true）。
3. correct_refusal：仅不可回答题可能为 true；回答资料不足且没有编造具体事实才为 true。
4. hallucination：答案包含参考资料和实际引用证据无法支持的具体事实，或不可回答题编造答案时为 true。

问题：%s
是否不可回答：%t
参考答案：%s
实际答案：%s
实际引用证据：
%s`, q.Question, q.Unanswerable, q.ReferenceAnswer, streamed.Answer, evidence.String())
	for attempt := 0; attempt < 3; attempt++ {
		stream, err := judge.Stream(ctx, model.ChatRequest{SystemPrompt: "你是严格的 RAG 评测裁判，必须只输出合法 JSON。", Messages: []model.ChatMessage{{Role: "user", Content: prompt}}})
		if err != nil {
			if attempt == 2 {
				return judgeResult{}, model.Usage{}, err
			}
			continue
		}
		var answer strings.Builder
		var used model.Usage
		for event := range stream {
			if event.Err != nil {
				err = event.Err
				break
			}
			answer.WriteString(event.Delta)
			if event.Usage != nil {
				used = *event.Usage
			}
		}
		if err != nil {
			if attempt == 2 {
				return judgeResult{}, used, err
			}
			continue
		}
		text := answer.String()
		start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
		if start >= 0 && end > start {
			var result judgeResult
			if json.Unmarshal([]byte(text[start:end+1]), &result) == nil {
				return result, used, nil
			}
		}
	}
	return judgeResult{}, model.Usage{}, errors.New("DeepSeek judge did not return valid JSON after three attempts")
}

func calculateMetrics(cases []caseResult, questions []question) metrics {
	var result metrics
	if len(cases) == 0 {
		return result
	}
	byID := map[string]question{}
	for _, item := range questions {
		byID[item.ID] = item
	}
	var answerable, unanswerable, correct, supported, refused, hallucinated, citationDenom int
	retrievalLatencies, endLatencies := []float64{}, []float64{}
	for _, item := range cases {
		q := byID[item.ID]
		if !q.Unanswerable {
			answerable++
			citationDenom++
			if item.FirstRelevantRank == 1 {
				result.RecallAt1++
			}
			if item.FirstRelevantRank > 0 && item.FirstRelevantRank <= 5 {
				result.RecallAt5++
			}
			if item.FirstRelevantRank > 0 && item.FirstRelevantRank <= 10 {
				result.RecallAt10++
			}
			if item.FirstRelevantRank > 0 {
				result.MRR += 1 / float64(item.FirstRelevantRank)
			}
			if item.CitationHit {
				result.CitationHitRate++
			}
			if item.Judge.AnswerCorrect {
				correct++
			}
			if item.Judge.EvidenceSupported {
				supported++
			}
		} else {
			unanswerable++
			if item.Judge.CorrectRefusal {
				refused++
			}
		}
		if item.Judge.Hallucination {
			hallucinated++
		}
		result.TotalTokens += float64(item.TotalTokens)
		result.TotalCostUSD += item.TotalCostUSD
		retrievalLatencies = append(retrievalLatencies, item.Retrieval.LatencyMS)
		endLatencies = append(endLatencies, item.EndToEndLatencyMS)
	}
	if answerable > 0 {
		n := float64(answerable)
		result.RecallAt1 /= n
		result.RecallAt5 /= n
		result.RecallAt10 /= n
		result.MRR /= n
		result.AnswerAccuracy = float64(correct) / n
		result.EvidenceSupportRate = float64(supported) / n
	}
	if citationDenom > 0 {
		result.CitationHitRate /= float64(citationDenom)
	}
	if unanswerable > 0 {
		result.CorrectRefusalRate = float64(refused) / float64(unanswerable)
	}
	result.HallucinationRate = float64(hallucinated) / float64(len(cases))
	result.AverageTokens = result.TotalTokens / float64(len(cases))
	result.AverageCostUSD = result.TotalCostUSD / float64(len(cases))
	result.AverageRetrievalLatencyMS = average(retrievalLatencies)
	result.P95RetrievalLatencyMS = percentile(retrievalLatencies, .95)
	result.AverageEndToEndLatencyMS = average(endLatencies)
	result.P95EndToEndLatencyMS = percentile(endLatencies, .95)
	return result
}

func calculateFormats(cases []caseResult, questions []question) []formatMetric {
	byID := map[string]question{}
	for _, item := range questions {
		byID[item.ID] = item
	}
	formats := []string{"pdf", "docx", "markdown", "txt"}
	result := make([]formatMetric, 0, len(formats))
	for _, format := range formats {
		metric := formatMetric{Format: format}
		var correct, support, refused, hallucinations, answerable, unanswerable int
		for _, item := range cases {
			q := byID[item.ID]
			if !contains(q.Tags, format) {
				continue
			}
			metric.Cases++
			if q.Unanswerable {
				unanswerable++
				if item.Judge.CorrectRefusal {
					refused++
				}
			} else {
				answerable++
				if item.Judge.AnswerCorrect {
					correct++
				}
				if item.Judge.EvidenceSupported {
					support++
				}
			}
			if item.Judge.Hallucination {
				hallucinations++
			}
			success := item.Error == "" && !item.Judge.Hallucination && ((q.Unanswerable && item.Judge.CorrectRefusal) || (!q.Unanswerable && item.Judge.AnswerCorrect && item.Judge.EvidenceSupported))
			if success {
				metric.Succeeded++
			} else {
				metric.FailureCases = append(metric.FailureCases, item.ID+": "+failureReason(item, q))
			}
		}
		if metric.Cases > 0 {
			metric.SuccessRate = float64(metric.Succeeded) / float64(metric.Cases)
			metric.HallucinationRate = float64(hallucinations) / float64(metric.Cases)
		}
		if answerable > 0 {
			metric.AnswerAccuracy = float64(correct) / float64(answerable)
			metric.EvidenceSupportRate = float64(support) / float64(answerable)
		}
		if unanswerable > 0 {
			metric.CorrectRefusalRate = float64(refused) / float64(unanswerable)
		}
		result = append(result, metric)
	}
	return result
}

func failureReason(item caseResult, q question) string {
	parts := []string{}
	if item.Error != "" {
		parts = append(parts, item.Error)
	}
	if q.Unanswerable && !item.Judge.CorrectRefusal {
		parts = append(parts, "未正确拒答")
	}
	if !q.Unanswerable && !item.Judge.AnswerCorrect {
		parts = append(parts, "答案不正确")
	}
	if !q.Unanswerable && !item.Judge.EvidenceSupported {
		parts = append(parts, "引用证据不支持")
	}
	if item.Judge.Hallucination {
		parts = append(parts, "幻觉")
	}
	return strings.Join(parts, "；")
}

func conclusions(rep report) []string {
	result := []string{"本报告是独立真实文档评测，不是 M5 确定性回归基线。"}
	for _, experiment := range rep.Experiments {
		result = append(result, fmt.Sprintf("%s：Recall@10 %.3f，答案正确率 %.3f，证据支持率 %.3f，拒答率 %.3f，幻觉率 %.3f。", experiment.Name, experiment.Metrics.RecallAt10, experiment.Metrics.AnswerAccuracy, experiment.Metrics.EvidenceSupportRate, experiment.Metrics.CorrectRefusalRate, experiment.Metrics.HallucinationRate))
	}
	return result
}

func validatePipeline(ctx context.Context, state runState) (pipelineValidation, error) {
	result := pipelineValidation{PublicUploadAPI: len(state.Documents) > 0}
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable", url.QueryEscape(envOr("POSTGRES_USER", "knowflow")), url.QueryEscape(envOr("POSTGRES_PASSWORD", "knowflow-dev-password")), envOr("POSTGRES_PORT", "5432"), url.PathEscape(envOr("POSTGRES_DB", "knowflow")))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return result, err
	}
	defer pool.Close()
	var uploadedDocuments int
	if err = pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE d.status='ready'), COALESCE(sum(d.chunk_count),0) FROM documents d WHERE d.knowledge_base_id=$1 AND d.deleted_at IS NULL`, state.KnowledgeBaseID).Scan(&uploadedDocuments, &result.ReadyDocuments, &result.ChunkCount); err != nil {
		return result, err
	}
	if uploadedDocuments != len(state.Documents) {
		return result, fmt.Errorf("database contains %d uploaded evaluation documents, expected %d", uploadedDocuments, len(state.Documents))
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_jobs j JOIN documents d ON d.id=j.document_id AND d.index_version=j.index_version WHERE d.knowledge_base_id=$1 AND d.deleted_at IS NULL AND j.status='succeeded'`, state.KnowledgeBaseID).Scan(&result.WorkerSucceeded); err != nil {
		return result, err
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE vector_dims(embedding)=1024) FROM document_chunks WHERE knowledge_base_id=$1`, state.KnowledgeBaseID).Scan(&result.Vector1024Count); err != nil {
		return result, err
	}
	rows, err := pool.Query(ctx, `SELECT object_key FROM documents WHERE knowledge_base_id=$1 AND deleted_at IS NULL`, state.KnowledgeBaseID)
	if err != nil {
		return result, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		_ = rows.Scan(&key)
		keys = append(keys, key)
	}
	rows.Close()
	minioClient, err := minio.New("127.0.0.1:"+envOr("MINIO_PORT", "9000"), &minio.Options{Creds: credentials.NewStaticV4(envOr("MINIO_ACCESS_KEY", "knowflow-dev"), envOr("MINIO_SECRET_KEY", "knowflow-dev-password"), ""), Secure: false})
	if err != nil {
		return result, err
	}
	for _, key := range keys {
		if _, err := minioClient.StatObject(ctx, envOr("MINIO_BUCKET", "knowflow"), key, minio.StatObjectOptions{}); err != nil {
			return result, err
		}
		result.MinIOObjects++
	}
	redisDB, _ := strconv.Atoi(envOr("REDIS_DB", "0"))
	rdb := redisclient.NewClient(&redisclient.Options{Addr: "127.0.0.1:" + envOr("REDIS_PORT", "6379"), Password: os.Getenv("REDIS_PASSWORD"), DB: redisDB})
	defer rdb.Close()
	length, err := rdb.LLen(ctx, "knowflow:ingestion").Result()
	if err != nil {
		return result, err
	}
	result.RedisQueueEmpty = length == 0
	usageRows, err := pool.Query(ctx, `SELECT request_type, model, count(*), COALESCE(sum(prompt_tokens),0), COALESCE(sum(completion_tokens),0), COALESCE(sum(text_count),0), COALESCE(sum(estimated_cost_usd),0), count(*) FILTER (WHERE status<>'succeeded') FROM model_usage WHERE knowledge_base_id=$1 GROUP BY request_type,model ORDER BY request_type,model`, state.KnowledgeBaseID)
	if err != nil {
		return result, err
	}
	defer usageRows.Close()
	for usageRows.Next() {
		var item providerUsageSummary
		if err := usageRows.Scan(&item.RequestType, &item.Model, &item.Calls, &item.PromptTokens, &item.CompletionTokens, &item.TextCount, &item.EstimatedCostUSD, &item.Failed); err != nil {
			return result, err
		}
		result.ProviderUsage = append(result.ProviderUsage, item)
	}
	return result, usageRows.Err()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (c *apiClient) health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.baseURL, "/api/v1")+"/api/v1/health/ready", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("public API health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("public API is not ready: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *apiClient) json(ctx context.Context, method, path string, input, output any) error {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if attempt == 4 {
				return err
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < 4 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		var wrapped envelope
		if err := json.Unmarshal(body, &wrapped); err != nil {
			return fmt.Errorf("decode HTTP %d: %w", resp.StatusCode, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if wrapped.Error != nil {
				return fmt.Errorf("%s: %s", wrapped.Error.Code, wrapped.Error.Message)
			}
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if output != nil {
			return json.Unmarshal(wrapped.Data, output)
		}
		return nil
	}
	return errors.New("request retries exhausted")
}

func (c *apiClient) upload(ctx context.Context, kbID, path string) (documentState, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return documentState{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filepath.Base(path)))
	header.Set("Content-Type", mimeType(path))
	part, err := writer.CreatePart(header)
	if err != nil {
		return documentState{}, err
	}
	_, _ = part.Write(content)
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/knowledge-bases/"+kbID+"/documents", &body)
	if err != nil {
		return documentState{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return documentState{}, err
	}
	defer resp.Body.Close()
	var wrapped envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&wrapped); err != nil {
		return documentState{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if wrapped.Error != nil {
			return documentState{}, fmt.Errorf("%s: %s", wrapped.Error.Code, wrapped.Error.Message)
		}
		return documentState{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data struct {
		Document documentState `json:"document"`
	}
	if err := json.Unmarshal(wrapped.Data, &data); err != nil {
		return documentState{}, err
	}
	data.Document.Filename = filepath.Base(path)
	data.Document.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	return data.Document, nil
}

func (c *apiClient) stream(ctx context.Context, conversationID, question string) (streamResult, error) {
	payload, _ := json.Marshal(map[string]string{"content": question})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/conversations/"+conversationID+"/messages", bytes.NewReader(payload))
	if err != nil {
		return streamResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return streamResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var wrapped envelope
		_ = json.NewDecoder(resp.Body).Decode(&wrapped)
		if wrapped.Error != nil {
			return streamResult{}, fmt.Errorf("%s: %s", wrapped.Error.Code, wrapped.Error.Message)
		}
		return streamResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	result := streamResult{EventNames: map[string]bool{}}
	var event string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			result.EventNames[event] = true
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		switch event {
		case "message.delta":
			var delta struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(data, &delta)
			result.Answer += delta.Delta
		case "retrieval.completed":
			_ = json.Unmarshal(data, &result.Trace)
		case "citation":
			var item citation
			if json.Unmarshal(data, &item) == nil {
				result.Citations = append(result.Citations, item)
			}
		case "usage":
			_ = json.Unmarshal(data, &result.Usage)
		case "message.completed":
			_ = json.Unmarshal(data, &result.Completed)
		case "error":
			return result, fmt.Errorf("SSE error: %s", data)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	for _, required := range []string{"message.started", "retrieval.completed", "message.delta", "usage", "message.completed"} {
		if !result.EventNames[required] {
			return result, fmt.Errorf("missing SSE event %s", required)
		}
	}
	return result, nil
}

func mimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".md":
		return "text/markdown"
	default:
		return "text/plain"
	}
}

func writeReports(rep report, jsonPath, markdownPath string) error {
	if err := writeJSON(jsonPath, rep); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(markdownPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(markdownPath, []byte(markdown(rep)), 0644)
}

func markdown(rep report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n生成时间：`%s`  \n数据集：`%s`  \n文档：**%d** 份；问题：**%d** 道。\n\n", rep.Title, rep.GeneratedAt.Format(time.RFC3339), rep.Dataset, rep.DocumentCount, rep.QuestionCount)
	b.WriteString("## 评测性质\n\n本报告是**真实文档评测**。`eval/datasets/knowflow-m5.jsonl` 的 60 题仅作为**确定性回归基线**，其接近满分结果不得直接称为真实文档质量评测。\n\n")
	fmt.Fprintf(&b, "真实 Provider：DeepSeek `%s`；Embedding `%s`；Reranker `%s`（AK/SK）。\n\n", rep.Providers.LLMModel, rep.Providers.EmbeddingModel, rep.Providers.RerankModel)
	b.WriteString("## 端到端链路验证\n\n| 公共上传 API | MinIO 对象 | Redis 队列已清空 | Worker 成功 | Ready 文档 | pgvector 分块 / 1024维向量 |\n|---:|---:|---:|---:|---:|---:|\n")
	fmt.Fprintf(&b, "| %t | %d | %t | %d | %d | %d / %d |\n\n", rep.Pipeline.PublicUploadAPI, rep.Pipeline.MinIOObjects, rep.Pipeline.RedisQueueEmpty, rep.Pipeline.WorkerSucceeded, rep.Pipeline.ReadyDocuments, rep.Pipeline.ChunkCount, rep.Pipeline.Vector1024Count)
	b.WriteString("## 汇总指标\n\n| 策略 | 全真实 | Recall@1/5/10 | MRR | 引用命中 | 答案正确 | 证据支持 | 正确拒答 | 幻觉率 | Retrieval P95 ms | E2E P95 ms | Avg Token | Total Cost USD |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range rep.Experiments {
		m := e.Metrics
		fmt.Fprintf(&b, "| %s | %t | %.3f / %.3f / %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.1f | %.1f | %.1f | %.6f |\n", e.Name, e.FullReal, m.RecallAt1, m.RecallAt5, m.RecallAt10, m.MRR, m.CitationHitRate, m.AnswerAccuracy, m.EvidenceSupportRate, m.CorrectRefusalRate, m.HallucinationRate, m.P95RetrievalLatencyMS, m.P95EndToEndLatencyMS, m.AverageTokens, m.TotalCostUSD)
	}
	for _, e := range rep.Experiments {
		b.WriteString("\n### " + e.Name + "：格式成功率\n\n| 格式 | 成功/总数 | 成功率 | 答案正确 | 证据支持 | 正确拒答 | 幻觉率 |\n|---|---:|---:|---:|---:|---:|---:|\n")
		for _, f := range e.Formats {
			fmt.Fprintf(&b, "| %s | %d/%d | %.3f | %.3f | %.3f | %.3f | %.3f |\n", strings.ToUpper(f.Format), f.Succeeded, f.Cases, f.SuccessRate, f.AnswerAccuracy, f.EvidenceSupportRate, f.CorrectRefusalRate, f.HallucinationRate)
		}
		b.WriteString("\n失败案例：\n\n")
		failures := 0
		for _, f := range e.Formats {
			for _, item := range f.FailureCases {
				fmt.Fprintf(&b, "- %s: %s\n", strings.ToUpper(f.Format), item)
				failures++
			}
		}
		if failures == 0 {
			b.WriteString("- 无。\n")
		}
	}
	b.WriteString("\n## Provider 调用记录\n\n| 类型 | 模型 | 调用 | Prompt Token | Completion Token | 文本数 | 记录成本 USD | 失败 |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, u := range rep.Pipeline.ProviderUsage {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %.6f | %d |\n", u.RequestType, u.Model, u.Calls, u.PromptTokens, u.CompletionTokens, u.TextCount, u.EstimatedCostUSD, u.Failed)
	}
	b.WriteString("\n## 结论\n\n")
	for _, item := range rep.Conclusions {
		b.WriteString("- " + item + "\n")
	}
	b.WriteString("\n> Cost 使用 `.env` 中 `EVAL_*_COST_PER_MILLION_USD` 的配置值计算，仅用于本次报告，不代表 Provider 官方价格声明。完整逐题答案、引用、证据、判定和失败原因见独立 JSON 报告。\n")
	return b.String()
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0644)
}
func readJSON(path string, value any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}
func normalize(value string) string {
	replacer := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "", "`", "")
	return replacer.Replace(value)
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
