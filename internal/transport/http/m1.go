package transporthttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"knowflow/internal/apperror"
	"knowflow/internal/auth"
	"knowflow/internal/knowledgebase"
	"knowflow/internal/platform/requestid"
)

type contextKey string

const userContextKey contextKey = "authenticated-user"

func registerM1Routes(mux *http.ServeMux, logger *slog.Logger, services BusinessServices) {
	mux.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var input auth.Credentials
		if !decodeJSON(w, r, &input) {
			return
		}
		pair, err := services.Auth.Register(r.Context(), input)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusCreated, pair)
	})
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var input auth.Credentials
		if !decodeJSON(w, r, &input) {
			return
		}
		if services.RateLimiter != nil {
			blocked, retryAfter, limitErr := services.RateLimiter.LoginBlocked(r.Context(), remoteIP(r), strings.ToLower(strings.TrimSpace(input.Email)))
			if limitErr != nil {
				WriteError(w, r, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "rate limiter is unavailable")
				return
			}
			if blocked {
				w.Header().Set("Retry-After", retryAfterHeader(retryAfter))
				WriteError(w, r, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "too many failed login attempts")
				return
			}
		}
		pair, err := services.Auth.Login(r.Context(), input)
		if err != nil {
			if services.RateLimiter != nil {
				_ = services.RateLimiter.RecordLoginFailure(r.Context(), remoteIP(r), strings.ToLower(strings.TrimSpace(input.Email)))
			}
			writeServiceError(w, r, logger, err)
			return
		}
		if services.RateLimiter != nil {
			_ = services.RateLimiter.ResetLogin(r.Context(), remoteIP(r), strings.ToLower(strings.TrimSpace(input.Email)))
		}
		WriteSuccess(w, r, http.StatusOK, pair)
	})
	mux.HandleFunc("POST /api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RefreshToken string `json:"refresh_token"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		pair, err := services.Auth.Refresh(r.Context(), input.RefreshToken)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, pair)
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RefreshToken string `json:"refresh_token"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if err := services.Auth.Logout(r.Context(), input.RefreshToken); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]bool{"revoked": true})
	})

	requireAuth := func(handler http.HandlerFunc) http.HandlerFunc {
		return authenticate(services.Auth, services.RateLimiter, logger, handler)
	}
	mux.HandleFunc("GET /api/v1/me", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		WriteSuccess(w, r, http.StatusOK, currentUser(r))
	}))
	mux.HandleFunc("POST /api/v1/knowledge-bases", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var input knowledgebase.CreateInput
		if !decodeJSON(w, r, &input) {
			return
		}
		kb, err := services.KnowledgeBase.Create(r.Context(), currentUser(r).ID, input)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusCreated, kb)
	}))
	mux.HandleFunc("GET /api/v1/knowledge-bases", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := pageParams(w, r)
		if !ok {
			return
		}
		result, err := services.KnowledgeBase.List(r.Context(), currentUser(r).ID, page, pageSize)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/v1/knowledge-bases/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		kb, err := services.KnowledgeBase.Get(r.Context(), currentUser(r).ID, r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, kb)
	}))
	mux.HandleFunc("PATCH /api/v1/knowledge-bases/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var input knowledgebase.UpdateInput
		if !decodeJSON(w, r, &input) {
			return
		}
		kb, err := services.KnowledgeBase.Update(r.Context(), currentUser(r).ID, r.PathValue("id"), input)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, kb)
	}))
	mux.HandleFunc("DELETE /api/v1/knowledge-bases/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if err := services.KnowledgeBase.Delete(r.Context(), currentUser(r).ID, r.PathValue("id")); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusAccepted, map[string]string{"status": "deleting"})
	}))
	mux.HandleFunc("POST /api/v1/knowledge-bases/{id}/documents", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, services.MaxUploadSize+(1<<20))
		part, err := multipartFile(r)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		defer part.Close()
		doc, duplicate, err := services.Document.Upload(r.Context(), currentUser(r).ID, r.PathValue("id"), part.FileName(), part.Header.Get("Content-Type"), part)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		status := http.StatusCreated
		if duplicate {
			status = http.StatusOK
		}
		WriteSuccess(w, r, status, map[string]any{"document": doc, "duplicate": duplicate})
	}))
	mux.HandleFunc("GET /api/v1/knowledge-bases/{id}/documents", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := pageParams(w, r)
		if !ok {
			return
		}
		result, err := services.Document.List(r.Context(), currentUser(r).ID, r.PathValue("id"), page, pageSize)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/v1/documents/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		doc, err := services.Document.Get(r.Context(), currentUser(r).ID, r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, doc)
	}))
	mux.HandleFunc("GET /api/v1/documents/{id}/chunks", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := pageParams(w, r)
		if !ok {
			return
		}
		result, err := services.Document.Chunks(r.Context(), currentUser(r).ID, r.PathValue("id"), page, pageSize)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/v1/documents/{id}/retry", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		doc, err := services.Document.Retry(r.Context(), currentUser(r).ID, r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusAccepted, doc)
	}))
	mux.HandleFunc("DELETE /api/v1/documents/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if err := services.Document.Delete(r.Context(), currentUser(r).ID, r.PathValue("id")); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusAccepted, map[string]string{"status": "deleting"})
	}))
}

func authenticate(service *auth.Service, limiter RateLimiter, logger *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		prefix, token, found := strings.Cut(header, " ")
		if !found || !strings.EqualFold(prefix, "Bearer") || strings.TrimSpace(token) == "" {
			WriteError(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "a Bearer access token is required")
			return
		}
		user, err := service.Authenticate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		if limiter != nil {
			allowed, retryAfter, limitErr := limiter.AllowUser(r.Context(), user.ID)
			if limitErr != nil {
				WriteError(w, r, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "rate limiter is unavailable")
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", retryAfterHeader(retryAfter))
				WriteError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests")
				return
			}
		}
		next(w, r.WithContext(contextWithUser(r, user)))
	}
}

func contextWithUser(r *http.Request, user auth.User) context.Context {
	return context.WithValue(r.Context(), userContextKey, user)
}

func currentUser(r *http.Request) auth.User {
	user, _ := r.Context().Value(userContextKey).(auth.User)
	return user
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		WriteError(w, r, http.StatusBadRequest, "INVALID_JSON", "request body must be valid JSON")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteError(w, r, http.StatusBadRequest, "INVALID_JSON", "request body must contain one JSON value")
		return false
	}
	return true
}

func multipartFile(r *http.Request) (*multipart.Part, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, apperror.New(http.StatusBadRequest, "INVALID_MULTIPART", "request must be multipart/form-data")
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperror.Wrap(http.StatusBadRequest, "INVALID_MULTIPART", "multipart upload could not be read", err)
		}
		if part.FormName() == "file" && part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
	return nil, apperror.New(http.StatusBadRequest, "FILE_REQUIRED", "multipart field file is required")
}

func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	parse := func(name string, fallback int) (int, bool) {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			return fallback, true
		}
		value, err := strconv.Atoi(raw)
		return value, err == nil && value > 0
	}
	page, okPage := parse("page", 1)
	pageSize, okSize := parse("page_size", 20)
	if !okPage || !okSize {
		WriteError(w, r, http.StatusBadRequest, "INVALID_PAGINATION", "page and page_size must be positive integers")
		return 0, 0, false
	}
	return page, pageSize, true
}

func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if appErr, ok := apperror.As(err); ok {
		if appErr.HTTPStatus >= 500 {
			logger.Error("request failed", "request_id", requestid.FromContext(r.Context()), "error_code", appErr.Code, "error", err)
		}
		WriteError(w, r, appErr.HTTPStatus, appErr.Code, appErr.Message)
		return
	}
	logger.Error("request failed", "request_id", requestid.FromContext(r.Context()), "error_code", "INTERNAL_ERROR", "error", err)
	WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
}
