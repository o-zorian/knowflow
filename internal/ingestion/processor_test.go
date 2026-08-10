package ingestion

import (
	"context"
	"io"
	"strings"
	"testing"

	"knowflow/internal/knowledgebase"
	"knowflow/internal/model"
)

type recordingJobStore struct {
	item       WorkItem
	claimed    bool
	stages     []string
	completed  []Chunk
	failedCode string
}

func (s *recordingJobStore) Claim(context.Context, Message) (WorkItem, bool, error) {
	return s.item, s.claimed, nil
}
func (s *recordingJobStore) UpdateStage(_ context.Context, _ WorkItem, stage, _ string, _ int) error {
	s.stages = append(s.stages, stage)
	return nil
}
func (s *recordingJobStore) Complete(_ context.Context, _ WorkItem, chunks []Chunk) error {
	s.completed = chunks
	return nil
}
func (s *recordingJobStore) Fail(_ context.Context, _ WorkItem, _ string, code, _ string) error {
	s.failedCode = code
	return nil
}

type staticObjectReader struct{ content string }

func (s staticObjectReader) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.content)), nil
}

func TestProcessorRunsStagesAndBatchesEmbeddings(t *testing.T) {
	config := knowledgebase.DefaultRetrievalConfig()
	config.ChunkSize, config.ChunkOverlap = 100, 20
	store := &recordingJobStore{claimed: true, item: WorkItem{Filename: "notes.txt", RetrievalConfig: config}}
	embedder, _ := model.NewFakeEmbedder(8)
	processor, err := NewProcessor(store, staticObjectReader{content: strings.Repeat("KnowFlow sentence. ", 40)}, DocumentParser{}, embedder, 2)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := processor.Process(context.Background(), Message{})
	if err != nil {
		t.Fatal(err)
	}
	if !processed || len(store.completed) < 2 || store.failedCode != "" {
		t.Fatalf("processor did not complete: processed=%v chunks=%d failure=%s", processed, len(store.completed), store.failedCode)
	}
	if len(store.stages) < 3 || store.stages[0] != "chunking" || store.stages[1] != "embedding" {
		t.Fatalf("unexpected stage sequence: %v", store.stages)
	}
	for _, chunk := range store.completed {
		if len(chunk.Embedding) != 8 {
			t.Fatalf("chunk has invalid embedding: %d", len(chunk.Embedding))
		}
	}
}

func TestProcessorIgnoresDuplicateClaim(t *testing.T) {
	store := &recordingJobStore{claimed: false}
	embedder, _ := model.NewFakeEmbedder(8)
	processor, _ := NewProcessor(store, staticObjectReader{}, DocumentParser{}, embedder, 2)
	processed, err := processor.Process(context.Background(), Message{})
	if err != nil || processed || len(store.stages) != 0 || len(store.completed) != 0 {
		t.Fatalf("duplicate job caused side effects: processed=%v err=%v", processed, err)
	}
}
