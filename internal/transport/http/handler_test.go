package transporthttp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"knowflow/internal/health"
	"knowflow/internal/platform/requestid"
)

func testHandler(dependencies []health.Dependency) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(logger, []string{"http://localhost:5173"}, time.Second, dependencies)
}

func TestLivenessUsesEnvelopeAndRequestID(t *testing.T) {
	handler := testHandler(nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		RequestID string `json:"request_id"`
		Data      struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !requestid.Valid(body.RequestID) || body.Data.Status != "alive" {
		t.Fatalf("body = %#v", body)
	}
	if body.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatal("response request IDs differ")
	}
}

func TestRequestIDIsPropagated(t *testing.T) {
	handler := testHandler(nil)
	want := "b1717e9e-0e85-4d75-b6a7-7697e2d24c8a"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	request.Header.Set("X-Request-ID", want)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); got != want {
		t.Fatalf("request ID = %q, want %q", got, want)
	}
}

func TestReadinessFailureUsesErrorEnvelope(t *testing.T) {
	handler := testHandler([]health.Dependency{{Name: "postgres", Check: func(context.Context) error { return context.DeadlineExceeded }}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "SERVICE_UNAVAILABLE" || !requestid.Valid(body.RequestID) {
		t.Fatalf("body = %#v", body)
	}
}

func TestUnknownRouteAndWrongMethodUseErrorEnvelope(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/api/v1/health/live", http.StatusMethodNotAllowed},
	} {
		response := httptest.NewRecorder()
		testHandler(nil).ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("%s %s: status=%d content-type=%q", test.method, test.path, response.Code, response.Header().Get("Content-Type"))
		}
	}
}
