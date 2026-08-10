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
