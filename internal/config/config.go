package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const EmbeddingDimension = 1024

type Secret string

func (s Secret) Value() string  { return string(s) }
func (Secret) String() string   { return "[REDACTED]" }
func (Secret) GoString() string { return "[REDACTED]" }

type Config struct {
	App         App
	HTTP        HTTP
	Database    Database
	Redis       Redis
	MinIO       MinIO
	Auth        Auth
	Models      Models
	Upload      Upload
	Log         Log
	Operational Operational
	Governance  Governance
	Pricing     Pricing
}

type App struct {
	Environment string
}

type HTTP struct {
	Addr           string
	AllowedOrigins []string
}

type Database struct {
	URL      Secret
	MaxConns int32
}

type Redis struct {
	Addr     string
	Password Secret
	DB       int
}

type MinIO struct {
	Endpoint  string
	AccessKey Secret
	SecretKey Secret
	Bucket    string
	UseSSL    bool
}

type Auth struct {
	JWTSecret       Secret
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type Models struct {
	LLM       Model
	Embedding EmbeddingModel
	Reranker  RerankModel
}

type Model struct {
	BaseURL string
	APIKey  Secret
	Name    string
}

type EmbeddingModel struct {
	Model
	Dimension int
	BatchSize int
}

const (
	RerankProviderOpenAICompatible = "openai-compatible"
	RerankProviderVikingDB         = "vikingdb"
)

// RerankModel supports both the legacy Bearer-token /rerank protocol and
// VikingDB Knowledge Rerank, which uses Volcengine AK/SK request signing.
type RerankModel struct {
	Model
	Provider string
	VikingDB VikingDBRerank
}

type VikingDBRerank struct {
	AccessKey Secret
	SecretKey Secret
	Host      string
	Region    string
}

type Upload struct {
	MaxSizeBytes int64
}

type Log struct {
	Level string
}

type Operational struct {
	StartupTimeout       time.Duration
	HealthCheckTimeout   time.Duration
	ShutdownTimeout      time.Duration
	WorkerHealthInterval time.Duration
	WorkerPollTimeout    time.Duration
	IngestionJobTimeout  time.Duration
	ModelMaxRetries      int
}

type Governance struct {
	IPRequestsPerMinute   int
	UserRequestsPerMinute int
	LoginFailures         int
	LoginFailureWindow    time.Duration
}

type Pricing struct {
	ChatInputPerMillion   float64
	ChatOutputPerMillion  float64
	EmbeddingPerMillion   float64
	RerankInputPerMillion float64
}

type scope struct {
	http, redis, minio, auth bool
}

func LoadAPI() (Config, error) {
	return load(scope{http: true, redis: true, minio: true, auth: true})
}

func LoadWorker() (Config, error) {
	return load(scope{redis: true, minio: true})
}

func LoadMigration() (Config, error) {
	return load(scope{})
}

func load(required scope) (Config, error) {
	var problems []string
	require := func(name string) string {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			problems = append(problems, name+" is required")
		}
		return value
	}

	cfg := Config{}
	cfg.App.Environment = require("APP_ENV")
	cfg.Database.URL = Secret(require("DATABASE_URL"))
	cfg.Log.Level = valueOrDefault("LOG_LEVEL", "info")

	if required.http {
		cfg.HTTP.Addr = require("HTTP_ADDR")
		cfg.HTTP.AllowedOrigins = csv(valueOrDefault("CORS_ALLOWED_ORIGINS", "http://localhost:5173"))
	}
	if required.redis {
		cfg.Redis.Addr = require("REDIS_ADDR")
		cfg.Redis.Password = Secret(os.Getenv("REDIS_PASSWORD"))
	}
	if required.minio {
		cfg.MinIO.Endpoint = require("MINIO_ENDPOINT")
		cfg.MinIO.AccessKey = Secret(require("MINIO_ACCESS_KEY"))
		cfg.MinIO.SecretKey = Secret(require("MINIO_SECRET_KEY"))
		cfg.MinIO.Bucket = require("MINIO_BUCKET")
	}
	if required.auth {
		cfg.Auth.JWTSecret = Secret(require("JWT_SECRET"))
		cfg.Auth.AccessTokenTTL = parseDuration("ACCESS_TOKEN_TTL", 2*time.Hour, &problems)
		cfg.Auth.RefreshTokenTTL = parseDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour, &problems)
	}

	cfg.Models.LLM = Model{
		BaseURL: strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
		APIKey:  Secret(os.Getenv("LLM_API_KEY")),
		Name:    strings.TrimSpace(os.Getenv("LLM_MODEL")),
	}
	cfg.Models.Embedding = EmbeddingModel{
		Model: Model{
			BaseURL: strings.TrimSpace(os.Getenv("EMBEDDING_BASE_URL")),
			APIKey:  Secret(os.Getenv("EMBEDDING_API_KEY")),
			Name:    strings.TrimSpace(os.Getenv("EMBEDDING_MODEL")),
		},
	}
	cfg.Models.Reranker = RerankModel{
		Provider: strings.ToLower(strings.TrimSpace(os.Getenv("RERANK_PROVIDER"))),
		Model: Model{
			BaseURL: strings.TrimSpace(os.Getenv("RERANK_BASE_URL")),
			APIKey:  Secret(os.Getenv("RERANK_API_KEY")),
			Name:    strings.TrimSpace(os.Getenv("RERANK_MODEL")),
		},
		VikingDB: VikingDBRerank{
			AccessKey: Secret(os.Getenv("VIKINGDB_AK")),
			SecretKey: Secret(os.Getenv("VIKINGDB_SK")),
			Host:      strings.TrimSpace(os.Getenv("VIKINGDB_HOST")),
			Region:    strings.TrimSpace(os.Getenv("VIKINGDB_REGION")),
		},
	}
	if cfg.Models.Reranker.Provider == "" {
		switch {
		case rerankVikingDBCredentialsConfigured(cfg.Models.Reranker.VikingDB):
			cfg.Models.Reranker.Provider = RerankProviderVikingDB
		case rerankBearerConfigured(cfg.Models.Reranker.Model):
			cfg.Models.Reranker.Provider = RerankProviderOpenAICompatible
		}
	}

	cfg.Database.MaxConns = int32(parseInt("DATABASE_MAX_CONNS", 10, 1, 100, &problems))
	cfg.Redis.DB = parseInt("REDIS_DB", 0, 0, 15, &problems)
	cfg.MinIO.UseSSL = parseBool("MINIO_USE_SSL", false, &problems)
	cfg.Models.Embedding.Dimension = parseInt("EMBEDDING_DIMENSION", EmbeddingDimension, 1, 65535, &problems)
	cfg.Models.Embedding.BatchSize = parseInt("EMBEDDING_BATCH_SIZE", 32, 1, 512, &problems)
	maxUploadMB := parseInt("MAX_UPLOAD_SIZE_MB", 30, 1, 1024, &problems)
	cfg.Upload.MaxSizeBytes = int64(maxUploadMB) * 1024 * 1024
	cfg.Operational.StartupTimeout = parseDuration("STARTUP_TIMEOUT", 30*time.Second, &problems)
	cfg.Operational.HealthCheckTimeout = parseDuration("HEALTH_CHECK_TIMEOUT", 2*time.Second, &problems)
	cfg.Operational.ShutdownTimeout = parseDuration("SHUTDOWN_TIMEOUT", 10*time.Second, &problems)
	cfg.Operational.WorkerHealthInterval = parseDuration("WORKER_HEALTH_INTERVAL", 30*time.Second, &problems)
	cfg.Operational.WorkerPollTimeout = parseDuration("WORKER_POLL_TIMEOUT", 2*time.Second, &problems)
	cfg.Operational.IngestionJobTimeout = parseDuration("INGESTION_JOB_TIMEOUT", 10*time.Minute, &problems)
	cfg.Operational.ModelMaxRetries = parseInt("MODEL_MAX_RETRIES", 3, 0, 10, &problems)
	cfg.Governance.IPRequestsPerMinute = parseInt("RATE_LIMIT_IP_PER_MINUTE", 300, 1, 100000, &problems)
	cfg.Governance.UserRequestsPerMinute = parseInt("RATE_LIMIT_USER_PER_MINUTE", 120, 1, 100000, &problems)
	cfg.Governance.LoginFailures = parseInt("LOGIN_FAILURE_LIMIT", 5, 1, 1000, &problems)
	cfg.Governance.LoginFailureWindow = parseDuration("LOGIN_FAILURE_WINDOW", 15*time.Minute, &problems)
	cfg.Pricing.ChatInputPerMillion = parseFloat("CHAT_INPUT_COST_PER_MILLION_USD", 0, &problems)
	cfg.Pricing.ChatOutputPerMillion = parseFloat("CHAT_OUTPUT_COST_PER_MILLION_USD", 0, &problems)
	cfg.Pricing.EmbeddingPerMillion = parseFloat("EMBEDDING_COST_PER_MILLION_USD", 0, &problems)
	cfg.Pricing.RerankInputPerMillion = parseFloat("RERANK_COST_PER_MILLION_USD", 0, &problems)

	validate(cfg, required, &problems)
	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

func validate(cfg Config, required scope, problems *[]string) {
	switch cfg.App.Environment {
	case "development", "test", "staging", "production":
	case "":
	default:
		*problems = append(*problems, "APP_ENV must be one of development, test, staging, production")
	}

	if cfg.Database.URL.Value() != "" {
		u, err := url.Parse(cfg.Database.URL.Value())
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.Path == "" {
			*problems = append(*problems, "DATABASE_URL must be a valid postgres URL")
		}
	}
	if required.http && cfg.HTTP.Addr != "" {
		if _, _, err := net.SplitHostPort(cfg.HTTP.Addr); err != nil {
			*problems = append(*problems, "HTTP_ADDR must be in host:port form")
		}
		if len(cfg.HTTP.AllowedOrigins) == 0 {
			*problems = append(*problems, "CORS_ALLOWED_ORIGINS must contain at least one origin")
		}
	}
	if required.redis && cfg.Redis.Addr != "" {
		if _, _, err := net.SplitHostPort(cfg.Redis.Addr); err != nil {
			*problems = append(*problems, "REDIS_ADDR must be in host:port form")
		}
	}
	if required.minio && cfg.MinIO.Endpoint != "" {
		if strings.Contains(cfg.MinIO.Endpoint, "://") || strings.ContainsAny(cfg.MinIO.Endpoint, "/?#") {
			*problems = append(*problems, "MINIO_ENDPOINT must be host:port without a URL scheme or path")
		} else if _, _, err := net.SplitHostPort(cfg.MinIO.Endpoint); err != nil {
			*problems = append(*problems, "MINIO_ENDPOINT must be in host:port form")
		}
	}
	if cfg.Models.Embedding.Dimension != EmbeddingDimension {
		*problems = append(*problems, fmt.Sprintf("EMBEDDING_DIMENSION must be %d to match the database migration", EmbeddingDimension))
	}
	validateModel("LLM", cfg.Models.LLM, problems)
	validateModel("EMBEDDING", cfg.Models.Embedding.Model, problems)
	validateReranker(cfg.Models.Reranker, problems)
	if cfg.App.Environment == "production" && required.http {
		if cfg.Models.LLM.BaseURL == "" {
			*problems = append(*problems, "LLM_BASE_URL, LLM_API_KEY, and LLM_MODEL are required in production")
		}
		if cfg.Models.Embedding.BaseURL == "" {
			*problems = append(*problems, "EMBEDDING_BASE_URL, EMBEDDING_API_KEY, and EMBEDDING_MODEL are required in production")
		}
	}
	if cfg.App.Environment == "production" {
		if required.auth && (cfg.Auth.JWTSecret.Value() == "change-me" || len(cfg.Auth.JWTSecret.Value()) < 32) {
			*problems = append(*problems, "JWT_SECRET must be at least 32 characters and not a development placeholder in production")
		}
		if required.minio && (cfg.MinIO.AccessKey.Value() == "change-me" || cfg.MinIO.SecretKey.Value() == "change-me") {
			*problems = append(*problems, "MinIO credentials must not use development placeholders in production")
		}
		for _, origin := range cfg.HTTP.AllowedOrigins {
			if origin == "*" {
				*problems = append(*problems, "CORS_ALLOWED_ORIGINS must not contain * in production")
			}
		}
	}
	if cfg.Log.Level != "debug" && cfg.Log.Level != "info" && cfg.Log.Level != "warn" && cfg.Log.Level != "error" {
		*problems = append(*problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}
}

func validateModel(prefix string, model Model, problems *[]string) {
	values := []string{model.BaseURL, model.APIKey.Value(), model.Name}
	configured := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(values) {
		*problems = append(*problems, prefix+"_BASE_URL, "+prefix+"_API_KEY, and "+prefix+"_MODEL must be configured together")
	}
	if model.BaseURL != "" {
		parsed, err := url.Parse(model.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			*problems = append(*problems, prefix+"_BASE_URL must be an absolute HTTP(S) URL")
		}
	}
}

func validateReranker(reranker RerankModel, problems *[]string) {
	switch reranker.Provider {
	case "":
		return
	case RerankProviderOpenAICompatible:
		validateModel("RERANK", reranker.Model, problems)
		if rerankVikingDBConfigured(reranker.VikingDB) {
			*problems = append(*problems, "VIKINGDB_AK, VIKINGDB_SK, VIKINGDB_HOST, and VIKINGDB_REGION cannot be combined with RERANK_PROVIDER=openai-compatible")
		}
	case RerankProviderVikingDB:
		missing := make([]string, 0, 5)
		if reranker.VikingDB.AccessKey.Value() == "" {
			missing = append(missing, "VIKINGDB_AK")
		}
		if reranker.VikingDB.SecretKey.Value() == "" {
			missing = append(missing, "VIKINGDB_SK")
		}
		if reranker.VikingDB.Host == "" {
			missing = append(missing, "VIKINGDB_HOST")
		}
		if reranker.VikingDB.Region == "" {
			missing = append(missing, "VIKINGDB_REGION")
		}
		if reranker.Name == "" {
			missing = append(missing, "RERANK_MODEL")
		}
		if len(missing) > 0 {
			*problems = append(*problems, strings.Join(missing, ", ")+" are required for RERANK_PROVIDER=vikingdb")
		}
		if reranker.BaseURL != "" || reranker.APIKey.Value() != "" {
			*problems = append(*problems, "RERANK_BASE_URL and RERANK_API_KEY cannot be combined with RERANK_PROVIDER=vikingdb")
		}
		if reranker.VikingDB.Host != "" {
			endpoint := reranker.VikingDB.Host
			if !strings.Contains(endpoint, "://") {
				endpoint = "https://" + endpoint
			}
			parsed, err := url.Parse(endpoint)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") {
				*problems = append(*problems, "VIKINGDB_HOST must be an HTTP(S) host without a path")
			}
		}
	default:
		*problems = append(*problems, "RERANK_PROVIDER must be openai-compatible or vikingdb")
	}
}

func rerankBearerConfigured(model Model) bool {
	return model.BaseURL != "" || model.APIKey.Value() != "" || model.Name != ""
}

func rerankVikingDBConfigured(viking VikingDBRerank) bool {
	return viking.AccessKey.Value() != "" || viking.SecretKey.Value() != "" || viking.Host != "" || viking.Region != ""
}

func rerankVikingDBCredentialsConfigured(viking VikingDBRerank) bool {
	return viking.AccessKey.Value() != "" || viking.SecretKey.Value() != ""
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func csv(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func parseInt(name string, fallback, min, max int, problems *[]string) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		*problems = append(*problems, fmt.Sprintf("%s must be an integer between %d and %d", name, min, max))
		return fallback
	}
	return value
}

func parseBool(name string, fallback bool, problems *[]string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		*problems = append(*problems, name+" must be a boolean")
		return fallback
	}
	return value
}

func parseDuration(name string, fallback time.Duration, problems *[]string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		*problems = append(*problems, name+" must be a positive Go duration such as 2s")
		return fallback
	}
	return value
}

func parseFloat(name string, fallback float64, problems *[]string) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		*problems = append(*problems, name+" must be a non-negative number")
		return fallback
	}
	return value
}
