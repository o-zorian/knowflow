package config

import (
	"strings"
	"testing"
)

func setValidAPIEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"APP_ENV":             "development",
		"HTTP_ADDR":           ":8080",
		"DATABASE_URL":        "postgres://knowflow:secret@localhost:5432/knowflow?sslmode=disable",
		"REDIS_ADDR":          "localhost:6379",
		"MINIO_ENDPOINT":      "localhost:9000",
		"MINIO_ACCESS_KEY":    "change-me",
		"MINIO_SECRET_KEY":    "change-me",
		"MINIO_BUCKET":        "knowflow",
		"JWT_SECRET":          "change-me",
		"EMBEDDING_DIMENSION": "1024",
		"LOG_LEVEL":           "info",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestLoadAPI(t *testing.T) {
	setValidAPIEnv(t)
	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if cfg.Models.Embedding.Dimension != 1024 {
		t.Fatalf("dimension = %d", cfg.Models.Embedding.Dimension)
	}
	if got := cfg.Database.URL.String(); got != "[REDACTED]" {
		t.Fatalf("secret String() = %q", got)
	}
}

func TestLoadAPIDoesNotExposeMissingSecretValue(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("JWT_SECRET", "")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET is required") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIRejectsDimensionMismatch(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("EMBEDDING_DIMENSION", "768")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "must be 1024") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIRejectsProductionPlaceholders(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("APP_ENV", "production")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") || !strings.Contains(err.Error(), "MinIO credentials") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIRejectsPartialModelConfiguration(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("LLM_BASE_URL", "https://models.example.test/v1")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "LLM_BASE_URL, LLM_API_KEY, and LLM_MODEL must be configured together") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIRejectsPartialRerankerConfiguration(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("RERANK_BASE_URL", "https://models.example.test/v1")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "RERANK_BASE_URL, RERANK_API_KEY, and RERANK_MODEL must be configured together") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPILoadsVikingDBReranker(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("RERANK_PROVIDER", "vikingdb")
	t.Setenv("RERANK_MODEL", "doubao-seed-rerank")
	t.Setenv("VIKINGDB_AK", "test-ak")
	t.Setenv("VIKINGDB_SK", "test-sk")
	t.Setenv("VIKINGDB_HOST", "api-knowledgebase.mlp.cn-beijing.volces.com")
	t.Setenv("VIKINGDB_REGION", "cn-beijing")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if cfg.Models.Reranker.Provider != RerankProviderVikingDB || cfg.Models.Reranker.Name != "doubao-seed-rerank" {
		t.Fatalf("reranker = %#v", cfg.Models.Reranker)
	}
	if got := cfg.Models.Reranker.VikingDB.AccessKey.String(); got != "[REDACTED]" {
		t.Fatalf("AK String() = %q", got)
	}
}

func TestLoadAPIInfersVikingDBReranker(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("RERANK_MODEL", "doubao-seed-rerank")
	t.Setenv("VIKINGDB_AK", "test-ak")
	t.Setenv("VIKINGDB_SK", "test-sk")
	t.Setenv("VIKINGDB_HOST", "https://api-knowledgebase.mlp.cn-beijing.volces.com")
	t.Setenv("VIKINGDB_REGION", "cn-beijing")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if cfg.Models.Reranker.Provider != RerankProviderVikingDB {
		t.Fatalf("provider = %q", cfg.Models.Reranker.Provider)
	}
}

func TestLoadAPIDoesNotEnableVikingDBFromEndpointDefaultsAlone(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("VIKINGDB_HOST", "api-knowledgebase.mlp.cn-beijing.volces.com")
	t.Setenv("VIKINGDB_REGION", "cn-beijing")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if cfg.Models.Reranker.Provider != "" {
		t.Fatalf("provider = %q", cfg.Models.Reranker.Provider)
	}
}

func TestLoadAPIRejectsPartialVikingDBReranker(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("RERANK_PROVIDER", "vikingdb")
	t.Setenv("RERANK_MODEL", "doubao-seed-rerank")
	t.Setenv("VIKINGDB_AK", "test-ak")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "VIKINGDB_SK") || !strings.Contains(err.Error(), "VIKINGDB_HOST") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIRejectsMixedRerankerCredentials(t *testing.T) {
	setValidAPIEnv(t)
	t.Setenv("RERANK_PROVIDER", "vikingdb")
	t.Setenv("RERANK_MODEL", "doubao-seed-rerank")
	t.Setenv("RERANK_API_KEY", "legacy-key")
	t.Setenv("VIKINGDB_AK", "test-ak")
	t.Setenv("VIKINGDB_SK", "test-sk")
	t.Setenv("VIKINGDB_HOST", "api-knowledgebase.mlp.cn-beijing.volces.com")
	t.Setenv("VIKINGDB_REGION", "cn-beijing")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}
