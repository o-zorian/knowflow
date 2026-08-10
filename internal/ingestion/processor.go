package ingestion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"knowflow/internal/model"
)

type ObjectReader interface {
	Get(ctx context.Context, objectKey string) (io.ReadCloser, error)
}

type Processor struct {
	store     JobStore
	objects   ObjectReader
	parser    Parser
	chunker   RecursiveChunker
	embedder  model.Embedder
	batchSize int
}

func NewProcessor(store JobStore, objects ObjectReader, parser Parser, embedder model.Embedder, batchSize int) (*Processor, error) {
	if store == nil || objects == nil || parser == nil || embedder == nil {
		return nil, errors.New("ingestion processor dependencies are required")
	}
	if batchSize <= 0 {
		return nil, errors.New("embedding batch size must be positive")
	}
	return &Processor{store: store, objects: objects, parser: parser, embedder: embedder, batchSize: batchSize}, nil
}

// Process returns false without side effects when the message represents an
// already-running or completed job.
func (p *Processor) Process(ctx context.Context, message Message) (bool, error) {
	item, claimed, err := p.store.Claim(ctx, message)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}
	stage := "parsing"
	fail := func(processing *ProcessingError) (bool, error) {
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if markErr := p.store.Fail(failureCtx, item, stage, processing.Code, processing.Message); markErr != nil {
			return true, fmt.Errorf("%w; mark ingestion failed: %v", processing, markErr)
		}
		return true, processing
	}

	object, err := p.objects.Get(ctx, item.ObjectKey)
	if err != nil {
		return fail(classify(err, "OBJECT_STORE_UNAVAILABLE", "source document is temporarily unavailable", true))
	}
	blocks, parseErr := p.parser.Parse(ctx, item.Filename, item.MIMEType, object)
	closeErr := object.Close()
	if parseErr != nil {
		return fail(classify(parseError(parseErr), "DOCUMENT_PARSE_FAILED", "document could not be parsed", false))
	}
	if closeErr != nil {
		return fail(classify(closeErr, "OBJECT_STORE_UNAVAILABLE", "source document is temporarily unavailable", true))
	}

	stage = "chunking"
	if err := p.store.UpdateStage(ctx, item, stage, "chunking", 35); err != nil {
		return fail(classify(err, "INDEX_STATE_UPDATE_FAILED", "indexing state could not be updated", true))
	}
	chunks, err := p.chunker.Chunk(blocks, item.RetrievalConfig.ChunkSize, item.RetrievalConfig.ChunkOverlap)
	if err != nil {
		return fail(classify(err, "DOCUMENT_CHUNKING_FAILED", "document could not be split into chunks", false))
	}

	stage = "embedding"
	if err := p.store.UpdateStage(ctx, item, stage, "embedding", 55); err != nil {
		return fail(classify(err, "INDEX_STATE_UPDATE_FAILED", "indexing state could not be updated", true))
	}
	for start := 0; start < len(chunks); start += p.batchSize {
		end := start + p.batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = chunks[index].Content
		}
		vectors, err := p.embedder.EmbedDocuments(ctx, texts)
		if err != nil {
			return fail(classify(err, "EMBEDDING_FAILED", "document embedding failed", true))
		}
		if len(vectors) != len(texts) {
			return fail(classify(errors.New("embedding count mismatch"), "EMBEDDING_INVALID_RESPONSE", "embedding provider returned an invalid response", true))
		}
		for index, vector := range vectors {
			if len(vector) != p.embedder.Dimension() {
				return fail(classify(errors.New("embedding dimension mismatch"), "EMBEDDING_INVALID_RESPONSE", "embedding provider returned an invalid response", true))
			}
			chunks[start+index].Embedding = vector
		}
		progress := 55 + end*35/len(chunks)
		if err := p.store.UpdateStage(ctx, item, stage, "embedding", progress); err != nil {
			return fail(classify(err, "INDEX_STATE_UPDATE_FAILED", "indexing state could not be updated", true))
		}
	}
	if err := p.store.Complete(ctx, item, chunks); err != nil {
		return fail(classify(err, "INDEX_PERSIST_FAILED", "document index could not be saved", true))
	}
	return true, nil
}
