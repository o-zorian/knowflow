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
	"knowflow/internal/config"
	"knowflow/internal/document"
	"knowflow/internal/health"
	"knowflow/internal/ingestion"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	"knowflow/internal/platform/objectstore"
	redisplatform "knowflow/internal/platform/redis"
	transporthttp "knowflow/internal/transport/http"
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
	authService := auth.NewService(auth.NewPostgresRepository(pool), cfg.Auth.JWTSecret.Value(), cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL)
	knowledgeBaseService := knowledgebase.NewService(knowledgebase.NewPostgresStore(pool), queue, cfg.Models.Embedding.Dimension)
	documentService := document.NewService(document.NewPostgresStore(pool), objectStore, queue, cfg.Upload.MaxSizeBytes)

	server := &http.Server{
		Addr: cfg.HTTP.Addr,
		Handler: transporthttp.NewHandler(logger, cfg.HTTP.AllowedOrigins, cfg.Operational.HealthCheckTimeout, dependencies,
			transporthttp.BusinessServices{
				Auth: authService, KnowledgeBase: knowledgeBaseService, Document: documentService, MaxUploadSize: cfg.Upload.MaxSizeBytes,
			}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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
