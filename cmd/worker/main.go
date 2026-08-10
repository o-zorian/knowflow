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
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	"knowflow/internal/platform/objectstore"
	redisplatform "knowflow/internal/platform/redis"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(cfg.Operational.WorkerHealthInterval)
	defer ticker.Stop()
	logger.Info("worker started", "environment", cfg.App.Environment)
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopped")
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

func checkDependencies(ctx context.Context, dependencies []health.Dependency) error {
	for name, err := range health.CheckAll(ctx, dependencies) {
		if err != nil {
			return fmt.Errorf("dependency %s is unavailable: %w", name, err)
		}
	}
	return nil
}
