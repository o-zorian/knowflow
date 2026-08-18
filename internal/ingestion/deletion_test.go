package ingestion

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

type deletionStoreStub struct {
	documentKeys       []string
	documentFound      bool
	knowledgeBaseKeys  []string
	knowledgeBaseFound bool
	completedDocument  string
	completedKB        string
}

func (s *deletionStoreStub) DocumentObjectKeys(context.Context, string, string) ([]string, bool, error) {
	return s.documentKeys, s.documentFound, nil
}
func (s *deletionStoreStub) CompleteDocumentDeletion(_ context.Context, _ string, id string) error {
	s.completedDocument = id
	return nil
}
func (s *deletionStoreStub) KnowledgeBaseObjectKeys(context.Context, string, string) ([]string, bool, error) {
	return s.knowledgeBaseKeys, s.knowledgeBaseFound, nil
}
func (s *deletionStoreStub) CompleteKnowledgeBaseDeletion(_ context.Context, _ string, id string) error {
	s.completedKB = id
	return nil
}

type objectRemoverStub struct {
	removed []string
	failKey string
}

func (s *objectRemoverStub) Remove(_ context.Context, key string) error {
	s.removed = append(s.removed, key)
	if key == s.failKey {
		return errors.New("object store unavailable")
	}
	return nil
}

func TestDeletionProcessorCleansDocumentObjectBeforeDatabase(t *testing.T) {
	ownerID, documentID := uuid.NewString(), uuid.NewString()
	store := &deletionStoreStub{documentKeys: []string{"raw/document"}, documentFound: true}
	objects := &objectRemoverStub{}
	processor, err := NewDeletionProcessor(store, objects)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := processor.Process(context.Background(), Message{
		Type: MessageDocumentDelete, OwnerID: ownerID, DocumentID: documentID,
	})
	if err != nil || !processed {
		t.Fatalf("deletion failed: processed=%v err=%v", processed, err)
	}
	if !reflect.DeepEqual(objects.removed, []string{"raw/document"}) || store.completedDocument != documentID {
		t.Fatalf("unexpected cleanup: removed=%v completed=%q", objects.removed, store.completedDocument)
	}
}

func TestDeletionProcessorCleansEveryKnowledgeBaseObject(t *testing.T) {
	ownerID, knowledgeBaseID := uuid.NewString(), uuid.NewString()
	store := &deletionStoreStub{knowledgeBaseKeys: []string{"raw/one", "raw/two"}, knowledgeBaseFound: true}
	objects := &objectRemoverStub{}
	processor, _ := NewDeletionProcessor(store, objects)
	processed, err := processor.Process(context.Background(), Message{
		Type: MessageKnowledgeBaseDelete, OwnerID: ownerID, KnowledgeBaseID: knowledgeBaseID,
	})
	if err != nil || !processed {
		t.Fatalf("deletion failed: processed=%v err=%v", processed, err)
	}
	if !reflect.DeepEqual(objects.removed, []string{"raw/one", "raw/two"}) || store.completedKB != knowledgeBaseID {
		t.Fatalf("unexpected cleanup: removed=%v completed=%q", objects.removed, store.completedKB)
	}
}

func TestDeletionProcessorDoesNotFinalizeAfterObjectFailure(t *testing.T) {
	store := &deletionStoreStub{documentKeys: []string{"raw/document"}, documentFound: true}
	processor, _ := NewDeletionProcessor(store, &objectRemoverStub{failKey: "raw/document"})
	processed, err := processor.Process(context.Background(), Message{
		Type: MessageDocumentDelete, OwnerID: uuid.NewString(), DocumentID: uuid.NewString(),
	})
	if err == nil || processed || store.completedDocument != "" {
		t.Fatalf("object failure was not preserved: processed=%v err=%v completed=%q", processed, err, store.completedDocument)
	}
}

func TestDeletionProcessorRejectsMalformedMessage(t *testing.T) {
	processor, _ := NewDeletionProcessor(&deletionStoreStub{}, &objectRemoverStub{})
	_, err := processor.Process(context.Background(), Message{Type: MessageDocumentDelete, OwnerID: uuid.NewString()})
	if !errors.Is(err, ErrInvalidDeletionMessage) {
		t.Fatalf("expected invalid message error, got %v", err)
	}
}
