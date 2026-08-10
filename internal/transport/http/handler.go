package transporthttp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"knowflow/internal/auth"
	"knowflow/internal/chat"
	"knowflow/internal/document"
	"knowflow/internal/health"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/platform/requestid"
)

type BusinessServices struct {
	Auth          *auth.Service
	KnowledgeBase *knowledgebase.Service
	Document      *document.Service
	Chat          *chat.Service
	MaxUploadSize int64
}

func NewHandler(logger *slog.Logger, allowedOrigins []string, timeout time.Duration, dependencies []health.Dependency, business ...BusinessServices) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health/live", func(w http.ResponseWriter, r *http.Request) {
		WriteSuccess(w, r, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("/api/v1/health/live", methodNotAllowed)
	mux.HandleFunc("GET /api/v1/health/ready", readinessHandler(logger, timeout, dependencies))
	mux.HandleFunc("/api/v1/health/ready", methodNotAllowed)
	if len(business) > 0 {
		registerM1Routes(mux, logger, business[0])
		if business[0].Chat != nil {
			registerM3Routes(mux, logger, business[0])
		}
	}
	mux.HandleFunc("/", notFound)

	var handler http.Handler = mux
	handler = corsMiddleware(allowedOrigins, handler)
	handler = recoveryMiddleware(logger, handler)
	handler = accessLogMiddleware(logger, handler)
	handler = requestIDMiddleware(handler)
	return handler
}

func readinessHandler(logger *slog.Logger, timeout time.Duration, dependencies []health.Dependency) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		results := health.CheckAll(ctx, dependencies)
		checks := make(map[string]string, len(results))
		ready := true
		for name, err := range results {
			if err != nil {
				ready = false
				checks[name] = "failed"
				logger.Warn("readiness dependency failed", "request_id", requestid.FromContext(r.Context()), "dependency", name, "error", err)
			} else {
				checks[name] = "ok"
			}
		}
		if !ready {
			WriteError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "one or more required dependencies are unavailable")
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
}

func notFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "resource not found")
}
