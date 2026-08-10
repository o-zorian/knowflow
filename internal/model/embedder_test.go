package model

import (
	"context"
	"reflect"
	"testing"
)

func TestFakeEmbedderIsDeterministicAndDimensioned(t *testing.T) {
	embedder, err := NewFakeEmbedder(16)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embedder.EmbedDocuments(context.Background(), []string{"alpha", "alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 || len(vectors[0]) != 16 || !reflect.DeepEqual(vectors[0], vectors[1]) || reflect.DeepEqual(vectors[0], vectors[2]) {
		t.Fatalf("unexpected fake embeddings: %#v", vectors)
	}
}

func TestFakeEmbedderRewardsSharedLexicalFeatures(t *testing.T) {
	embedder, _ := NewFakeEmbedder(1024)
	vectors, err := embedder.EmbedDocuments(context.Background(), []string{
		"KnowFlow uses SSE streaming citations",
		"unrelated weather forecast",
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := embedder.EmbedQuery(context.Background(), "SSE streaming")
	if err != nil {
		t.Fatal(err)
	}
	cosine := func(left, right []float32) float32 {
		var score float32
		for index := range left {
			score += left[index] * right[index]
		}
		return score
	}
	if cosine(query, vectors[0]) <= cosine(query, vectors[1]) {
		t.Fatal("shared lexical evidence was not ranked first")
	}
}
