package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlerServesAssetsAndSPAFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>KnowFlow</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(root, "http://api.example.test/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/knowledge-bases", "/health", "/config.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, response.Code)
		}
		if path == "/config.js" && !strings.Contains(response.Body.String(), "http://api.example.test/api/v1") {
			t.Fatal("runtime API configuration was not rendered")
		}
	}
}
