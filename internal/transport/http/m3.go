package transporthttp

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"knowflow/internal/chat"
)

func registerM3Routes(mux *http.ServeMux, logger *slog.Logger, services BusinessServices) {
	requireAuth := func(handler http.HandlerFunc) http.HandlerFunc {
		return authenticate(services.Auth, logger, handler)
	}
	mux.HandleFunc("POST /api/v1/conversations", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			KnowledgeBaseID string `json:"knowledge_base_id"`
			Title           string `json:"title"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		conversation, err := services.Chat.Create(r.Context(), currentUser(r).ID, input.KnowledgeBaseID, input.Title)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusCreated, conversation)
	}))
	mux.HandleFunc("GET /api/v1/conversations", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := pageParams(w, r)
		if !ok {
			return
		}
		result, err := services.Chat.List(r.Context(), currentUser(r).ID, page, pageSize)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/v1/conversations/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		detail, err := services.Chat.Get(r.Context(), currentUser(r).ID, r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, detail)
	}))
	mux.HandleFunc("DELETE /api/v1/conversations/{id}", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if err := services.Chat.Delete(r.Context(), currentUser(r).ID, r.PathValue("id")); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]bool{"deleted": true})
	}))
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Content string `json:"content"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		events, err := services.Chat.Stream(r.Context(), currentUser(r).ID, r.PathValue("id"), input.Content)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		for event := range events {
			if err := writeSSE(w, event); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}))
}

func writeSSE(w http.ResponseWriter, event chat.StreamEvent) error {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + event.Name + "\n")); err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
