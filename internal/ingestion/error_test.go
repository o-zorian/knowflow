package ingestion

import (
	"errors"
	"testing"
)

func TestProcessingErrorClassifiesRetryability(t *testing.T) {
	if classify(permanent("EMPTY_DOCUMENT", "empty", nil), "fallback", "fallback", true).Retryable {
		t.Fatal("permanent parser error was classified as retryable")
	}
	if !classify(errors.New("timeout"), "EMBEDDING_FAILED", "embedding failed", true).Retryable {
		t.Fatal("external failure was not classified as retryable")
	}
}
