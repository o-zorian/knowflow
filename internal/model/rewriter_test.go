package model

import (
	"context"
	"errors"
	"testing"
)

func TestFakeQueryRewriterMakesFollowUpStandalone(t *testing.T) {
	rewriter := &FakeQueryRewriter{}
	result, err := rewriter.Rewrite(context.Background(), []ChatMessage{
		{Role: "user", Content: "KnowFlow 支持哪些检索方式？"},
		{Role: "assistant", Content: "支持向量与全文检索。"},
	}, "它如何融合结果？")
	if err != nil {
		t.Fatal(err)
	}
	if result != "KnowFlow 支持哪些检索方式？；后续问题：它如何融合结果？" {
		t.Fatalf("rewritten = %q", result)
	}
}

func TestFakeQueryRewriterFailure(t *testing.T) {
	rewriter := &FakeQueryRewriter{Failure: errors.New("provider unavailable")}
	if _, err := rewriter.Rewrite(context.Background(), nil, "query"); err == nil {
		t.Fatal("expected rewrite failure")
	}
}
