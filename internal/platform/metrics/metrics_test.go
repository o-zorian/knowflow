package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"knowflow/internal/usage"
)

func TestHandlerExposesRequiredMetricFamilies(t *testing.T) {
	m := New()
	m.ObserveHTTP("GET", "/test", 200, time.Millisecond)
	m.ObserveModel(usage.Entry{RequestType: "embedding", Model: "fake", Status: "succeeded", TextCount: 2, LatencyMS: 1})
	m.ObserveRetrieval("dense", time.Millisecond)
	m.SetIngestion("failed", 1)
	m.SetQueueLength(2)
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, name := range []string{"knowflow_http_requests_total", "knowflow_http_request_duration_seconds", "knowflow_model_requests_total", "knowflow_model_request_duration_seconds", "knowflow_embedding_texts_total", "knowflow_retrieval_duration_seconds", "knowflow_ingestion_jobs", "knowflow_ingestion_queue_length"} {
		if !strings.Contains(body, name) {
			t.Errorf("missing %s", name)
		}
	}
}
