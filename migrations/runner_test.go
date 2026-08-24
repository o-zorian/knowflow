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
	if len(migrations) != 5 || migrations[0].version != 1 || migrations[1].version != 2 || migrations[2].version != 3 || migrations[3].version != 4 || migrations[4].version != 5 {
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
	for _, required := range []string{"knowflow_sparse_query", "knowflow_search_primary_terms", "knowflow_search_fallback_terms", "setweight", "USING GIN"} {
		if !strings.Contains(migrations[2].sql, required) {
			t.Errorf("CJK sparse migration does not contain %q", required)
		}
	}
	for _, required := range []string{"CREATE OR REPLACE FUNCTION knowflow_sparse_query", "NULL::tsquery"} {
		if !strings.Contains(migrations[3].sql, required) {
			t.Errorf("empty sparse query migration does not contain %q", required)
		}
	}
	for _, required := range []string{"knowflow_sparse_coverage", "tsvector_to_array", "knowflow_search_primary_terms"} {
		if !strings.Contains(migrations[4].sql, required) {
			t.Errorf("sparse coverage migration does not contain %q", required)
		}
	}
}
