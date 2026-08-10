package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type client struct {
	baseURL string
	token   string
	http    *http.Client
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type tokens struct {
	AccessToken string `json:"access_token"`
}

type knowledgeBase struct {
	ID string `json:"id"`
}

type document struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ChunkCount int    `json:"chunk_count"`
	ErrorCode  string `json:"error_code"`
}

type conversation struct {
	ID string `json:"id"`
}

func main() {
	baseURL := flag.String("base-url", "http://localhost:8080/api/v1", "KnowFlow API v1 base URL")
	documentPath := flag.String("document", "demo/knowflow-demo.md", "demo document path")
	timeout := flag.Duration("timeout", 90*time.Second, "overall acceptance timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c := &client{baseURL: strings.TrimRight(*baseURL, "/"), http: &http.Client{Timeout: 15 * time.Second}}
	if err := run(ctx, c, *documentPath); err != nil {
		fmt.Fprintln(os.Stderr, "SMOKE FAILED:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c *client, documentPath string) error {
	email := fmt.Sprintf("release-smoke-%d@knowflow.local", time.Now().UnixNano())
	var pair tokens
	if err := c.json(ctx, http.MethodPost, "/auth/register", map[string]string{"email": email, "password": "release-smoke-password"}, &pair); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.token = pair.AccessToken
	fmt.Println("PASS register and login", email)

	var kb knowledgeBase
	if err := c.json(ctx, http.MethodPost, "/knowledge-bases", map[string]any{
		"name": "Release acceptance", "description": "Automated M6 acceptance knowledge base", "embedding_model": "fake-embedding",
	}, &kb); err != nil {
		return fmt.Errorf("create knowledge base: %w", err)
	}
	fmt.Println("PASS create knowledge base", kb.ID)

	doc, err := c.upload(ctx, kb.ID, documentPath)
	if err != nil {
		return fmt.Errorf("upload document: %w", err)
	}
	fmt.Println("PASS upload document", doc.ID)

	deadline := time.NewTicker(500 * time.Millisecond)
	defer deadline.Stop()
	for doc.Status != "ready" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for indexing: %w", ctx.Err())
		case <-deadline.C:
			if err := c.json(ctx, http.MethodGet, "/documents/"+doc.ID, nil, &doc); err != nil {
				return fmt.Errorf("poll document: %w", err)
			}
			if doc.Status == "failed" {
				return fmt.Errorf("indexing failed with %s", doc.ErrorCode)
			}
		}
	}
	if doc.ChunkCount < 1 {
		return errors.New("ready document has no chunks")
	}
	fmt.Printf("PASS asynchronous indexing ready (%d chunks)\n", doc.ChunkCount)

	var conv conversation
	if err := c.json(ctx, http.MethodPost, "/conversations", map[string]string{
		"knowledge_base_id": kb.ID, "title": "Release acceptance",
	}, &conv); err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	events, citations, answer, err := c.stream(ctx, conv.ID, "KnowFlow 支持哪些文档格式？")
	if err != nil {
		return fmt.Errorf("stream answer: %w", err)
	}
	required := []string{"message.started", "retrieval.completed", "message.delta", "citation", "usage", "message.completed"}
	for _, name := range required {
		if !events[name] {
			return fmt.Errorf("SSE event %q was not observed", name)
		}
	}
	if citations < 1 || !strings.Contains(answer, "[1]") {
		return errors.New("streamed answer did not contain a real citation")
	}
	fmt.Printf("PASS cited SSE answer (%d citation events)\n", citations)

	var detail struct {
		Messages []struct {
			Role      string            `json:"role"`
			Status    string            `json:"status"`
			Citations []json.RawMessage `json:"citations"`
		} `json:"messages"`
	}
	if err := c.json(ctx, http.MethodGet, "/conversations/"+conv.ID, nil, &detail); err != nil {
		return fmt.Errorf("reload conversation: %w", err)
	}
	persisted := false
	for _, message := range detail.Messages {
		if message.Role == "assistant" && message.Status == "completed" && len(message.Citations) > 0 {
			persisted = true
		}
	}
	if !persisted {
		return errors.New("completed cited answer was not persisted")
	}
	fmt.Println("PASS persisted answer and source citations")
	fmt.Println("SMOKE PASSED")
	return nil
}

func (c *client) json(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponse(response, output)
}

func (c *client) upload(ctx context.Context, kbID, path string) (document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return document{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filepath.Base(path)))
	header.Set("Content-Type", "text/markdown")
	part, err := writer.CreatePart(header)
	if err != nil {
		return document{}, err
	}
	if _, err = part.Write(content); err != nil {
		return document{}, err
	}
	if err = writer.Close(); err != nil {
		return document{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/knowledge-bases/"+kbID+"/documents", &body)
	if err != nil {
		return document{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return document{}, err
	}
	defer response.Body.Close()
	var result struct {
		Document document `json:"document"`
	}
	if err := decodeResponse(response, &result); err != nil {
		return document{}, err
	}
	return result.Document, nil
}

func (c *client) stream(ctx context.Context, conversationID, question string) (map[string]bool, int, string, error) {
	payload, _ := json.Marshal(map[string]string{"content": question})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/conversations/"+conversationID+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0, "", decodeResponse(response, nil)
	}
	events := map[string]bool{}
	var current string
	var answer strings.Builder
	citations := 0
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			events[current] = true
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if current == "message.delta" {
				var delta struct {
					Delta string `json:"delta"`
				}
				if json.Unmarshal([]byte(data), &delta) == nil {
					answer.WriteString(delta.Delta)
				}
			}
			if current == "citation" {
				citations++
			}
			if current == "error" {
				return events, citations, answer.String(), fmt.Errorf("SSE error: %s", data)
			}
		}
	}
	return events, citations, answer.String(), scanner.Err()
}

func decodeResponse(response *http.Response, output any) error {
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	var wrapped envelope
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		return fmt.Errorf("decode HTTP %d response: %w", response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if wrapped.Error != nil {
			return fmt.Errorf("%s: %s", wrapped.Error.Code, wrapped.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(wrapped.Data, output)
}
