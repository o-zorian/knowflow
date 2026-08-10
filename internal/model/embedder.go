package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
)

type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Dimension() int
}

// FakeEmbedder is deterministic, offline, and intentionally free of provider
// calls. M2 uses it to exercise the complete pgvector ingestion path.
type FakeEmbedder struct {
	dimension int
}

func NewFakeEmbedder(dimension int) (*FakeEmbedder, error) {
	if dimension <= 0 {
		return nil, errors.New("embedding dimension must be positive")
	}
	return &FakeEmbedder{dimension: dimension}, nil
}

func (f *FakeEmbedder) Dimension() int { return f.dimension }

func (f *FakeEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors[i] = f.embed(text)
	}
	return vectors, nil
}

func (f *FakeEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.embed(text), nil
}

func (f *FakeEmbedder) embed(text string) []float32 {
	digest := sha256.Sum256([]byte(text))
	vector := make([]float32, f.dimension)
	var norm float64
	for i := range vector {
		word := binary.LittleEndian.Uint32(digest[(i%8)*4 : (i%8+1)*4])
		value := float32(int64(word)-(1<<31)) / float32(1<<31)
		vector[i] = value
		norm += float64(value * value)
		if i%8 == 7 {
			digest = sha256.Sum256(digest[:])
		}
	}
	if norm == 0 {
		vector[0] = 1
		return vector
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vector {
		vector[i] *= scale
	}
	return vector
}
