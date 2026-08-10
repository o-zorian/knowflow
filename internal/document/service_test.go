package document

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

type fakeDocumentStore struct {
	existing    *Document
	created     int
	markFailed  int
	document    Document
	chunks      ChunkPage
	createError error
}

func (s *fakeDocumentStore) FindDuplicate(_ context.Context, ownerID, knowledgeBaseID, _ string) (Document, error) {
	if s.existing != nil && ownerID == "owner-1" && knowledgeBaseID == "kb-1" {
		return *s.existing, nil
	}
	return Document{}, ErrNotFound
}
func (s *fakeDocumentStore) CreateDocumentAndJob(_ context.Context, _, knowledgeBaseID, _ string, prepared preparedUpload, documentID, _ string) (Document, bool, error) {
	s.created++
	if s.createError != nil {
		return Document{}, false, s.createError
	}
	return Document{ID: documentID, KnowledgeBaseID: knowledgeBaseID, Filename: prepared.filename, MIMEType: prepared.mimeType, SizeBytes: prepared.size, SHA256: prepared.sha256, Status: StatusQueued, IndexVersion: 1}, false, nil
}
func (s *fakeDocumentStore) MarkEnqueueFailed(context.Context, string, string, string) error {
	s.markFailed++
	return nil
}
func (*fakeDocumentStore) List(context.Context, string, string, int, int) (Page, error) {
	return Page{}, nil
}
func (s *fakeDocumentStore) Get(_ context.Context, ownerID, _ string) (Document, error) {
	if ownerID != "owner-1" {
		return Document{}, ErrNotFound
	}
	return s.document, nil
}
func (s *fakeDocumentStore) ListChunks(context.Context, string, string, int, int) (ChunkPage, error) {
	return s.chunks, nil
}
func (*fakeDocumentStore) PrepareRetry(context.Context, string, string) (Document, IngestionJob, error) {
	return Document{}, IngestionJob{}, ErrNotRetryable
}
func (*fakeDocumentStore) MarkDeleting(context.Context, string, string) error { return nil }

type fakeObjects struct {
	putCount    int
	removeCount int
	putError    error
	data        []byte
}

func (o *fakeObjects) Put(_ context.Context, _ string, reader io.Reader, _ int64, _ string) error {
	o.putCount++
	if o.putError != nil {
		return o.putError
	}
	o.data, _ = io.ReadAll(reader)
	return nil
}
func (o *fakeObjects) Remove(context.Context, string) error { o.removeCount++; return nil }

type fakeQueue struct{ indexes int }

func (q *fakeQueue) EnqueueIndex(context.Context, string, string, string, int) error {
	q.indexes++
	return nil
}
func (*fakeQueue) EnqueueDocumentDeletion(context.Context, string, string) error { return nil }

func TestPrepareUploadValidatesFilenameMIMESizeAndSHA256(t *testing.T) {
	contents := []byte("KnowFlow streaming digest\n")
	prepared, err := prepareUpload(bytes.NewReader(contents), "notes.md", "text/markdown; charset=utf-8", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(prepared.path)
	expected := fmt.Sprintf("%x", sha256.Sum256(contents))
	if prepared.sha256 != expected || prepared.size != int64(len(contents)) || prepared.mimeType != "text/markdown" {
		t.Fatalf("unexpected prepared upload: %+v", prepared)
	}

	badCases := []struct {
		name, filename, mime string
		data                 []byte
		limit                int64
	}{
		{"path traversal", "../notes.txt", "text/plain", contents, 1024},
		{"windows traversal", `..\notes.txt`, "text/plain", contents, 1024},
		{"unsupported extension", "notes.exe", "application/octet-stream", contents, 1024},
		{"mismatched declared mime", "notes.txt", "application/pdf", contents, 1024},
		{"oversize", "notes.txt", "text/plain", contents, 4},
		{"renamed pdf", "notes.pdf", "application/pdf", contents, 1024},
	}
	for _, test := range badCases {
		t.Run(test.name, func(t *testing.T) {
			if upload, err := prepareUpload(bytes.NewReader(test.data), test.filename, test.mime, test.limit); err == nil {
				_ = os.Remove(upload.path)
				t.Fatal("invalid upload was accepted")
			}
		})
	}
}

func TestPrepareUploadAcceptsActualDOCX(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte("<xml/>"))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareUpload(bytes.NewReader(buffer.Bytes()), "document.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", int64(buffer.Len()+1))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(prepared.path)
}

func TestDuplicateUploadSkipsObjectAndJob(t *testing.T) {
	existing := Document{ID: "existing", KnowledgeBaseID: "kb-1", Status: StatusQueued}
	store := &fakeDocumentStore{existing: &existing}
	objects, queue := &fakeObjects{}, &fakeQueue{}
	service := NewService(store, objects, queue, 1024)
	doc, duplicate, err := service.Upload(context.Background(), "owner-1", "kb-1", "notes.txt", "text/plain", bytes.NewBufferString("same content"))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || doc.ID != existing.ID || objects.putCount != 0 || store.created != 0 || queue.indexes != 0 {
		t.Fatalf("duplicate caused side effects: duplicate=%v puts=%d creates=%d queues=%d", duplicate, objects.putCount, store.created, queue.indexes)
	}
}

func TestObjectStoreFailureLeavesDatabaseUntouched(t *testing.T) {
	store := &fakeDocumentStore{}
	objects := &fakeObjects{putError: errors.New("minio unavailable")}
	service := NewService(store, objects, &fakeQueue{}, 1024)
	if _, _, err := service.Upload(context.Background(), "owner-1", "kb-1", "notes.txt", "text/plain", bytes.NewBufferString("content")); err == nil {
		t.Fatal("object store failure was ignored")
	}
	if store.created != 0 || objects.removeCount != 0 {
		t.Fatalf("database/object cleanup state is inconsistent: creates=%d removes=%d", store.created, objects.removeCount)
	}
}

func TestDocumentStatusAndChunkStageBehaviorIsOwnerScoped(t *testing.T) {
	store := &fakeDocumentStore{document: Document{ID: "doc-1", Status: StatusQueued}}
	service := NewService(store, &fakeObjects{}, &fakeQueue{}, 1024)
	if _, err := service.Get(context.Background(), "owner-2", "doc-1"); err == nil {
		t.Fatal("another owner accessed document status")
	}
	if _, err := service.Chunks(context.Background(), "owner-1", "doc-1", 1, 20); err == nil {
		t.Fatal("chunks were exposed before document readiness")
	}
	store.document.Status = StatusReady
	store.chunks = ChunkPage{Items: []Chunk{{ID: "chunk-1", Content: "preview"}}, Page: 1, PageSize: 20, Total: 1}
	page, err := service.Chunks(context.Background(), "owner-1", "doc-1", 1, 20)
	if err != nil || page.Total != 1 {
		t.Fatalf("ready chunk preview failed: page=%+v err=%v", page, err)
	}
}
