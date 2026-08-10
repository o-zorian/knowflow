package main

import (
	"context"
	"fmt"
	"os"

	"knowflow/internal/config"
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	"knowflow/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMigration()
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, "migrate")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Operational.StartupTimeout)
	defer cancel()
	pool, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		return err
	}
	logger.Info("database migrations are current")
	return nil
}
