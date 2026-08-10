package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	addr := valueOr("WEB_ADDR", ":8081")
	root := valueOr("WEB_ROOT", "/app/web")
	handler, err := newHandler(root, valueOr("WEB_API_BASE_URL", "http://localhost:8080/api/v1"))
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("KnowFlow Web listening on %s", addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func valueOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func newHandler(root string, apiBaseURL ...string) (http.Handler, error) {
	info, err := os.Stat(filepath.Join(root, "index.html"))
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("web release assets are missing from %s", root)
	}
	files := http.FileServer(http.Dir(root))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config.js", func(w http.ResponseWriter, _ *http.Request) {
		value := "http://localhost:8080/api/v1"
		if len(apiBaseURL) > 0 && strings.TrimSpace(apiBaseURL[0]) != "" {
			value = strings.TrimRight(strings.TrimSpace(apiBaseURL[0]), "/")
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "window.__KNOWFLOW_CONFIG__ = {apiBaseUrl: %q};\n", value)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		if r.URL.Path != "/" {
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
				http.Error(w, "web asset unavailable", http.StatusInternalServerError)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
	return mux, nil
}
