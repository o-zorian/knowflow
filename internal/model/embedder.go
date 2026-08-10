package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"strings"
	"unicode"
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
	vector := make([]float32, f.dimension)
	normalized := []rune(strings.ToLower(strings.TrimSpace(text)))
	features := make([]string, 0, len(normalized)*2)
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			features = append(features, word.String())
			word.Reset()
		}
	}
	for index, char := range normalized {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			word.WriteRune(char)
		} else {
			flushWord()
		}
		features = append(features, string(char))
		if index+1 < len(normalized) {
			features = append(features, string(normalized[index:index+2]))
		}
	}
	flushWord()
	if len(features) == 0 {
		features = append(features, "empty")
	}
	for _, feature := range features {
		digest := sha256.Sum256([]byte(feature))
		index := int(uint64(digest[0])<<8|uint64(digest[1])) % f.dimension
		vector[index]++
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value * value)
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
