package transporthttp

import (
	"encoding/json"
	"net/http"

	"knowflow/internal/platform/requestid"
)

type successResponse struct {
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

func WriteSuccess(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSON(w, status, successResponse{Data: data, RequestID: requestid.FromContext(r.Context())})
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error:     errorBody{Code: code, Message: message},
		RequestID: requestid.FromContext(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
