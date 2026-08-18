package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"knowflow/internal/config"
	"knowflow/internal/health"
	"knowflow/internal/ingestion"
	"knowflow/internal/model"
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	"knowflow/internal/platform/objectstore"
	redisplatform "knowflow/internal/platform/redis"
	"knowflow/internal/usage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, "worker")
	if err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), cfg.Operational.StartupTimeout)
	defer cancel()
	pool, err := database.Open(startupCtx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
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
	if err := checkDependencies(startupCtx, dependencies); err != nil {
		return err
	}
	embedder, err := configuredEmbedder(cfg)
	if err != nil {
		return err
	}
	embedderName := cfg.Models.Embedding.Name
	if embedderName == "" {
		embedderName = "fake-embedding"
	}
	pricing := usage.Pricing{EmbeddingPerMillion: cfg.Pricing.EmbeddingPerMillion}
	embedder = usage.ObserveEmbedder(embedder, embedderName, usage.NewPostgresRecorder(pool), pricing, nil)
	processor, err := ingestion.NewProcessor(ingestion.NewPostgresStore(pool), objectStore,
		ingestion.DocumentParser{}, embedder, cfg.Models.Embedding.BatchSize)
	if err != nil {
		return err
	}
	deletionProcessor, err := ingestion.NewDeletionProcessor(ingestion.NewPostgresStore(pool), objectStore)
	if err != nil {
		return err
	}
	queue := ingestion.NewRedisQueue(redisClient.Native())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	indexWorker := ingestion.NewWorker(queue, processor, deletionProcessor, logger,
		cfg.Operational.WorkerPollTimeout, cfg.Operational.IngestionJobTimeout)
	workerErrors := make(chan error, 1)
	go func() { workerErrors <- indexWorker.Run(ctx) }()
	ticker := time.NewTicker(cfg.Operational.WorkerHealthInterval)
	defer ticker.Stop()
	logger.Info("worker started", "environment", cfg.App.Environment)
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopped")
			return nil
		case err := <-workerErrors:
			if err != nil {
				return fmt.Errorf("run ingestion worker: %w", err)
			}
			return nil
		case <-ticker.C:
			checkCtx, checkCancel := context.WithTimeout(ctx, cfg.Operational.HealthCheckTimeout)
			results := health.CheckAll(checkCtx, dependencies)
			checkCancel()
			for name, checkErr := range results {
				if checkErr != nil {
					logger.Warn("worker dependency unavailable", "dependency", name, "error", checkErr)
				}
			}
		}
	}
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

func checkDependencies(ctx context.Context, dependencies []health.Dependency) error {
	for name, err := range health.CheckAll(ctx, dependencies) {
		if err != nil {
			return fmt.Errorf("dependency %s is unavailable: %w", name, err)
		}
	}
	return nil
}
