package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"knowflow/internal/auth"
	"knowflow/internal/chat"
	"knowflow/internal/config"
	"knowflow/internal/document"
	"knowflow/internal/governance"
	"knowflow/internal/health"
	"knowflow/internal/ingestion"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	platformmetrics "knowflow/internal/platform/metrics"
	"knowflow/internal/platform/objectstore"
	redisplatform "knowflow/internal/platform/redis"
	"knowflow/internal/retrieval"
	transporthttp "knowflow/internal/transport/http"
	"knowflow/internal/usage"
	"knowflow/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, "api")
	if err != nil {
		return err
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.Operational.StartupTimeout)
	defer startupCancel()
	pool, err := database.Open(startupCtx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrations.Apply(startupCtx, pool); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	logger.Info("database migrations are current")

	redisClient := redisplatform.New(cfg.Redis)
	defer func() { _ = redisClient.Close() }()
	objectStore, err := objectstore.New(cfg.MinIO)
	if err != nil {
		return err
	}
	dependencies := []health.Dependency{
		{Name: "postgres", Check: pool.Ping},
		{Name: "redis", Check: redisClient.Ping},
		{Name: "minio", Check: objectStore.Check},
	}
	queue := ingestion.NewRedisQueue(redisClient.Native())
	metrics := platformmetrics.New()
	pricing := usage.Pricing{ChatInputPerMillion: cfg.Pricing.ChatInputPerMillion, ChatOutputPerMillion: cfg.Pricing.ChatOutputPerMillion,
		EmbeddingPerMillion: cfg.Pricing.EmbeddingPerMillion, RerankInputPerMillion: cfg.Pricing.RerankInputPerMillion}
	usageRecorder := usage.NewPostgresRecorder(pool)
	governanceService := governance.NewService(pool, redisClient.Native(), metrics)
	limiter := governance.NewLimiter(redisClient.Native(), cfg.Governance)
	authService := auth.NewService(auth.NewPostgresRepository(pool), cfg.Auth.JWTSecret.Value(), cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL)
	knowledgeBaseService := knowledgebase.NewService(knowledgebase.NewPostgresStore(pool), queue, cfg.Models.Embedding.Dimension)
	documentService := document.NewService(document.NewPostgresStore(pool), objectStore, queue, cfg.Upload.MaxSizeBytes)
	embedder, err := configuredEmbedder(cfg)
	if err != nil {
		return err
	}
	embedderName := cfg.Models.Embedding.Name
	if embedderName == "" {
		embedderName = "fake-embedding"
	}
	embedder = usage.ObserveEmbedder(embedder, embedderName, usageRecorder, pricing, metrics)
	chatModel, chatModelName, err := configuredChatModel(cfg)
	if err != nil {
		return err
	}
	chatModel = usage.ObserveChat(chatModel, chatModelName, usageRecorder, pricing, metrics)
	reranker, err := configuredReranker(cfg)
	if err != nil {
		return err
	}
	rerankerName := cfg.Models.Reranker.Name
	if rerankerName == "" {
		rerankerName = "fake-reranker"
	}
	reranker = usage.ObserveReranker(reranker, rerankerName, usageRecorder, pricing, metrics)
	queryRewriter := configuredQueryRewriter(cfg, chatModel)
	retrievalService := retrieval.NewService(retrieval.NewPostgresStore(pool), embedder, reranker)
	retrievalService.SetObserver(metrics)
	chatService := chat.NewService(chat.NewPostgresStore(pool), retrievalService, chatModel, chatModelName, queryRewriter)
	chatService.SetPricing(pricing)

	server := &http.Server{
		Addr: cfg.HTTP.Addr,
		Handler: transporthttp.NewHandler(logger, cfg.HTTP.AllowedOrigins, cfg.Operational.HealthCheckTimeout, dependencies,
			transporthttp.BusinessServices{
				Auth: authService, KnowledgeBase: knowledgeBaseService, Document: documentService, Chat: chatService, MaxUploadSize: cfg.Upload.MaxSizeBytes,
				Governance: governanceService, RateLimiter: limiter, Metrics: metrics,
			}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("api server started", "addr", cfg.HTTP.Addr, "environment", cfg.App.Environment)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("api shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Operational.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful HTTP shutdown: %w", err)
	}
	logger.Info("api stopped")
	return nil
}

func configuredEmbedder(cfg config.Config) (model.Embedder, error) {
	if cfg.Models.Embedding.BaseURL == "" {
		return model.NewFakeEmbedder(cfg.Models.Embedding.Dimension)
	}
	client, err := model.NewOpenAIEmbeddingClient(cfg.Models.Embedding.BaseURL, cfg.Models.Embedding.APIKey.Value(),
		cfg.Models.Embedding.Name, cfg.Models.Embedding.Dimension, nil)
	if err == nil {
		client.SetMaxRetries(cfg.Operational.ModelMaxRetries)
	}
	return client, err
}

func configuredChatModel(cfg config.Config) (model.ChatModel, string, error) {
	if cfg.Models.LLM.BaseURL == "" {
		fake := &model.FakeChatModel{ModelName: "fake-chat"}
		return fake, fake.Name(), nil
	}
	client, err := model.NewOpenAIClient(cfg.Models.LLM.BaseURL, cfg.Models.LLM.APIKey.Value(), cfg.Models.LLM.Name, nil)
	if err != nil {
		return nil, "", err
	}
	client.SetMaxRetries(cfg.Operational.ModelMaxRetries)
	return client, client.Name(), nil
}

func configuredReranker(cfg config.Config) (model.Reranker, error) {
	switch cfg.Models.Reranker.Provider {
	case "":
		if cfg.App.Environment == "production" {
			return nil, nil
		}
		return &model.FakeReranker{}, nil
	case config.RerankProviderOpenAICompatible:
		client, err := model.NewRerankClient(cfg.Models.Reranker.BaseURL, cfg.Models.Reranker.APIKey.Value(), cfg.Models.Reranker.Name, nil)
		if err == nil {
			client.SetMaxRetries(cfg.Operational.ModelMaxRetries)
		}
		return client, err
	case config.RerankProviderVikingDB:
		viking := cfg.Models.Reranker.VikingDB
		client, err := model.NewVikingDBRerankClient(viking.Host, viking.Region, viking.AccessKey.Value(), viking.SecretKey.Value(), cfg.Models.Reranker.Name, nil)
		if err == nil {
			client.SetMaxRetries(cfg.Operational.ModelMaxRetries)
		}
		return client, err
	default:
		return nil, fmt.Errorf("unsupported rerank provider %q", cfg.Models.Reranker.Provider)
	}
}

func configuredQueryRewriter(cfg config.Config, chatModel model.ChatModel) model.QueryRewriter {
	if cfg.Models.LLM.BaseURL == "" {
		return &model.FakeQueryRewriter{}
	}
	return model.NewChatQueryRewriter(chatModel)
}
