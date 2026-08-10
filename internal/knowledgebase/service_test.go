package knowledgebase

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct {
	items map[string]KnowledgeBase
}

func (s *fakeStore) Create(_ context.Context, ownerID string, input CreateInput, config RetrievalConfig, dimension int) (KnowledgeBase, error) {
	key := ownerID + ":" + input.Name
	if _, exists := s.items[key]; exists {
		return KnowledgeBase{}, ErrNameExists
	}
	kb := KnowledgeBase{ID: key, OwnerID: ownerID, Name: input.Name, Description: input.Description, EmbeddingModel: input.EmbeddingModel, EmbeddingDimension: dimension, RetrievalConfig: config}
	s.items[key] = kb
	return kb, nil
}
func (*fakeStore) List(context.Context, string, int, int) (Page, error) { return Page{}, nil }
func (s *fakeStore) Get(_ context.Context, ownerID, id string) (KnowledgeBase, error) {
	for _, kb := range s.items {
		if kb.ID == id && kb.OwnerID == ownerID {
			return kb, nil
		}
	}
	return KnowledgeBase{}, ErrNotFound
}
func (*fakeStore) Update(context.Context, string, string, UpdateInput) (KnowledgeBase, error) {
	return KnowledgeBase{}, errors.New("unused")
}
func (*fakeStore) Delete(context.Context, string, string) error { return nil }

func TestDefaultsNameUniquenessAndOwnership(t *testing.T) {
	store := &fakeStore{items: map[string]KnowledgeBase{}}
	service := NewService(store, nil, 1024)
	first, err := service.Create(context.Background(), "user-1", CreateInput{Name: "Product", EmbeddingModel: "embed-test"})
	if err != nil {
		t.Fatal(err)
	}
	if first.RetrievalConfig != DefaultRetrievalConfig() {
		t.Fatalf("unexpected defaults: %+v", first.RetrievalConfig)
	}
	if _, err := service.Create(context.Background(), "user-1", CreateInput{Name: "Product", EmbeddingModel: "embed-test"}); err == nil {
		t.Fatal("same-owner duplicate name was accepted")
	}
	if _, err := service.Create(context.Background(), "user-2", CreateInput{Name: "Product", EmbeddingModel: "embed-test"}); err != nil {
		t.Fatalf("different owner should be able to reuse name: %v", err)
	}
	if _, err := service.Get(context.Background(), "user-2", first.ID); err == nil {
		t.Fatal("another owner accessed the knowledge base")
	}
}

func TestRetrievalConfigAllowsOneSourceToBeDisabled(t *testing.T) {
	config := DefaultRetrievalConfig()
	config.SparseTopK = 0
	if err := validateConfig(config); err != nil {
		t.Fatalf("dense-only config rejected: %v", err)
	}
	config.DenseTopK, config.SparseTopK = 0, 20
	if err := validateConfig(config); err != nil {
		t.Fatalf("sparse-only config rejected: %v", err)
	}
	config.SparseTopK = 0
	if err := validateConfig(config); err == nil {
		t.Fatal("config with both retrieval sources disabled was accepted")
	}
}
