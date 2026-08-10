package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"knowflow/internal/usage"
)

type Metrics struct {
	registry          *prometheus.Registry
	httpRequests      *prometheus.CounterVec
	httpDuration      *prometheus.HistogramVec
	modelRequests     *prometheus.CounterVec
	modelErrors       *prometheus.CounterVec
	modelDuration     *prometheus.HistogramVec
	embeddingTexts    prometheus.Counter
	retrievalDuration *prometheus.HistogramVec
	ingestionJobs     *prometheus.GaugeVec
	ingestionQueue    prometheus.Gauge
}

func New() *Metrics {
	m := &Metrics{
		registry:          prometheus.NewRegistry(),
		httpRequests:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "knowflow_http_requests_total", Help: "HTTP requests."}, []string{"method", "route", "status"}),
		httpDuration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "knowflow_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"method", "route"}),
		modelRequests:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "knowflow_model_requests_total", Help: "Model calls."}, []string{"type", "model", "status"}),
		modelErrors:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "knowflow_model_errors_total", Help: "Failed model calls."}, []string{"type", "model", "error_code"}),
		modelDuration:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "knowflow_model_request_duration_seconds", Help: "Model call latency.", Buckets: prometheus.DefBuckets}, []string{"type", "model"}),
		embeddingTexts:    prometheus.NewCounter(prometheus.CounterOpts{Name: "knowflow_embedding_texts_total", Help: "Texts sent for embedding."}),
		retrievalDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "knowflow_retrieval_duration_seconds", Help: "Retrieval latency.", Buckets: prometheus.DefBuckets}, []string{"strategy"}),
		ingestionJobs:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "knowflow_ingestion_jobs", Help: "Current ingestion jobs by status."}, []string{"status"}),
		ingestionQueue:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "knowflow_ingestion_queue_length", Help: "Current Redis ingestion queue length."}),
	}
	m.registry.MustRegister(m.httpRequests, m.httpDuration, m.modelRequests, m.modelErrors, m.modelDuration,
		m.embeddingTexts, m.retrievalDuration, m.ingestionJobs, m.ingestionQueue, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	for _, status := range []string{"pending", "running", "succeeded", "failed"} {
		m.ingestionJobs.WithLabelValues(status).Set(0)
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

func (m *Metrics) ObserveModel(entry usage.Entry) {
	m.modelRequests.WithLabelValues(entry.RequestType, entry.Model, entry.Status).Inc()
	m.modelDuration.WithLabelValues(entry.RequestType, entry.Model).Observe(float64(entry.LatencyMS) / 1000)
	if entry.Status == "failed" {
		m.modelErrors.WithLabelValues(entry.RequestType, entry.Model, entry.ErrorCode).Inc()
	}
	if entry.RequestType == "embedding" {
		m.embeddingTexts.Add(float64(entry.TextCount))
	}
}

func (m *Metrics) ObserveRetrieval(strategy string, elapsed time.Duration) {
	m.retrievalDuration.WithLabelValues(strategy).Observe(elapsed.Seconds())
}

func (m *Metrics) SetIngestion(status string, count float64) {
	m.ingestionJobs.WithLabelValues(status).Set(count)
}
func (m *Metrics) SetQueueLength(count float64) { m.ingestionQueue.Set(count) }
