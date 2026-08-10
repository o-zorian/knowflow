package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"knowflow/internal/config"
	"knowflow/internal/evaluation"
	"knowflow/internal/model"
	"knowflow/internal/platform/database"
	"knowflow/internal/platform/logging"
	"knowflow/internal/usage"
	"knowflow/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
}
func run() error {
	datasetPath := flag.String("dataset", "eval/datasets/knowflow-m5.jsonl", "JSONL evaluation dataset")
	jsonPath := flag.String("json", "eval/results/m5-comparison.json", "JSON report path")
	markdownPath := flag.String("markdown", "eval/results/m5-comparison.md", "Markdown report path")
	flag.Parse()
	cfg, err := config.LoadMigration()
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, "eval")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = migrations.Apply(ctx, pool); err != nil {
		return err
	}
	file, err := os.Open(*datasetPath)
	if err != nil {
		return err
	}
	questions, err := evaluation.LoadDataset(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	embedder, err := model.NewFakeEmbedder(config.EmbeddingDimension)
	if err != nil {
		return err
	}
	pricing := usage.Pricing{ChatInputPerMillion: cfg.Pricing.ChatInputPerMillion, ChatOutputPerMillion: cfg.Pricing.ChatOutputPerMillion, EmbeddingPerMillion: cfg.Pricing.EmbeddingPerMillion, RerankInputPerMillion: cfg.Pricing.RerankInputPerMillion}
	runner := evaluation.NewRunner(pool, embedder, &model.FakeReranker{}, &model.FakeChatModel{ModelName: "fake-chat"}, pricing)
	report, err := runner.Run(ctx, *datasetPath, questions)
	if err != nil {
		return err
	}
	if err = evaluation.WriteReports(report, *jsonPath, *markdownPath); err != nil {
		return err
	}
	fmt.Printf("evaluated %d questions across %d retrieval configurations\nJSON: %s\nMarkdown: %s\n", report.QuestionCount, len(report.Experiments), *jsonPath, *markdownPath)
	return nil
}
