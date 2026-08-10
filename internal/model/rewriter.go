package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type QueryRewriter interface {
	Rewrite(ctx context.Context, history []ChatMessage, query string) (string, error)
}

// ChatQueryRewriter uses the configured chat provider without coupling the
// chat service to any provider SDK.
type ChatQueryRewriter struct {
	model ChatModel
}

func NewChatQueryRewriter(chatModel ChatModel) *ChatQueryRewriter {
	return &ChatQueryRewriter{model: chatModel}
}

func (r *ChatQueryRewriter) Rewrite(ctx context.Context, history []ChatMessage, query string) (string, error) {
	if r == nil || r.model == nil {
		return "", errors.New("query rewriter model is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	messages := append([]ChatMessage(nil), history...)
	messages = append(messages, ChatMessage{Role: "user", Content: query})
	stream, err := r.model.Stream(ctx, ChatRequest{
		SystemPrompt: "Rewrite the latest user question as one self-contained search query using the conversation context. Return only the rewritten query. Preserve the user's language and intent. Do not answer the question.",
		Messages:     messages,
	})
	if err != nil {
		return "", err
	}
	var rewritten strings.Builder
	for event := range stream {
		if event.Err != nil {
			return "", event.Err
		}
		if rewritten.Len()+len(event.Delta) > 20000 {
			return "", errors.New("rewritten query is too long")
		}
		rewritten.WriteString(event.Delta)
	}
	result := strings.Trim(strings.TrimSpace(rewritten.String()), "\"'`")
	if result == "" {
		return "", errors.New("query rewriter returned an empty query")
	}
	return result, nil
}

// FakeQueryRewriter is deterministic and offline. For follow-up questions it
// attaches the most recent user subject; standalone questions pass through.
type FakeQueryRewriter struct {
	Failure error
}

func (f *FakeQueryRewriter) Rewrite(_ context.Context, history []ChatMessage, query string) (string, error) {
	if f != nil && f.Failure != nil {
		return "", f.Failure
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	if !looksLikeFollowUp(query) {
		return query, nil
	}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == "user" && strings.TrimSpace(history[index].Content) != "" {
			return fmt.Sprintf("%s；后续问题：%s", strings.TrimSpace(history[index].Content), query), nil
		}
	}
	return query, nil
}

func looksLikeFollowUp(query string) bool {
	lower := strings.ToLower(query)
	for _, marker := range []string{"它", "这个", "这项", "这些", "上述", "其中", "该", " it ", " this ", " that ", " they ", " those ", " these "} {
		if strings.Contains(" "+lower+" ", marker) {
			return true
		}
	}
	return false
}
