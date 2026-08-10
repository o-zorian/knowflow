package migrations

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreOrderedAndContainCoreSchema(t *testing.T) {
	migrations, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 2 || migrations[0].version != 1 || migrations[1].version != 2 {
		t.Fatalf("migrations = %#v", migrations)
	}
	for _, required := range []string{
		"CREATE EXTENSION IF NOT EXISTS vector",
		"CREATE TABLE users",
		"CREATE TABLE refresh_tokens",
		"CREATE TABLE knowledge_bases",
		"CREATE TABLE documents",
		"CREATE TABLE document_chunks",
		"CREATE TABLE ingestion_jobs",
		"CREATE TABLE conversations",
		"CREATE TABLE messages",
		"CREATE TABLE model_usage",
		"VECTOR(1024)",
		"USING hnsw",
		"USING GIN",
	} {
		if !strings.Contains(migrations[0].sql, required) {
			t.Errorf("migration does not contain %q", required)
		}
	}
	for _, required := range []string{"knowflow_search_terms", "USING GIN", "search_vector"} {
		if !strings.Contains(migrations[1].sql, required) {
			t.Errorf("M4 migration does not contain %q", required)
		}
	}
}
