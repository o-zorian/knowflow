package transporthttp

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	platformmetrics "knowflow/internal/platform/metrics"
	"knowflow/internal/platform/requestid"
)

type RateLimiter interface {
	AllowIP(context.Context, string) (bool, time.Duration, error)
	AllowUser(context.Context, string) (bool, time.Duration, error)
	LoginBlocked(context.Context, string, string) (bool, time.Duration, error)
	RecordLoginFailure(context.Context, string, string) error
	ResetLogin(context.Context, string, string) error
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !requestid.Valid(id) {
			id = requestid.New()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(requestid.WithContext(r.Context(), id)))
	})
}

func accessLogMiddleware(logger *slog.Logger, metrics *platformmetrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if metrics != nil {
			metrics.ObserveHTTP(r.Method, routeLabel(r), recorder.status, time.Since(started))
		}
		logger.Info("http request completed",
			"request_id", requestid.FromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func ipRateLimitMiddleware(limiter RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/api/v1/health/") {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter, err := limiter.AllowIP(r.Context(), remoteIP(r))
		if err != nil {
			WriteError(w, r, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "rate limiter is unavailable")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", retryAfterHeader(retryAfter))
			WriteError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func retryAfterHeader(duration time.Duration) string {
	seconds := int(duration.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func routeLabel(r *http.Request) string {
	if pattern := r.Pattern; pattern != "" {
		return pattern
	}
	return r.URL.Path
}

func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered", "request_id", requestid.FromContext(r.Context()))
				WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (slices.Contains(allowedOrigins, origin) || slices.Contains(allowedOrigins, "*")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
