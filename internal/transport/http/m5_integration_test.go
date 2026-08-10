package transporthttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/auth"
	"knowflow/internal/governance"
	platformmetrics "knowflow/internal/platform/metrics"
	"knowflow/internal/usage"
	"knowflow/migrations"
)

func TestM5AdminUsageMetricsAndDisableIntegration(t *testing.T) {
	databaseURL := os.Getenv("KNOWFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set KNOWFLOW_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := redisclient.NewClient(&redisclient.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	adminEmail, normalEmail := "m5-admin-"+uuid.NewString()+"@example.test", "m5-user-"+uuid.NewString()+"@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM model_usage WHERE model='m5-integration'`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE email=ANY($1)`, []string{adminEmail, normalEmail})
	})
	authService := auth.NewService(auth.NewPostgresRepository(pool), "integration-secret-at-least-32-bytes", 2*time.Hour, 24*time.Hour)
	metrics := platformmetrics.New()
	governanceService := governance.NewService(pool, redisClient, metrics)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(logger, []string{"http://localhost"}, time.Second, nil, BusinessServices{Auth: authService, Governance: governanceService, Metrics: metrics})
	adminTokens := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register", map[string]string{"email": adminEmail, "password": "correct horse battery staple"}, "", http.StatusCreated)
	normalTokens := requestTokenPair(t, handler, http.MethodPost, "/api/v1/auth/register", map[string]string{"email": normalEmail, "password": "correct horse battery staple"}, "", http.StatusCreated)
	if _, err = pool.Exec(ctx, `UPDATE users SET role='admin' WHERE id=$1`, adminTokens.User.ID); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/admin/metrics/summary", nil, normalTokens.AccessToken, http.StatusForbidden, nil)
	requestJSON(t, handler, http.MethodGet, "/api/v1/admin/metrics/summary", nil, adminTokens.AccessToken, http.StatusOK, nil)
	recorder := usage.NewPostgresRecorder(pool)
	if err = recorder.Record(usage.WithScope(ctx, adminTokens.User.ID, ""), usage.Entry{RequestType: "chat", Model: "m5-integration", PromptTokens: 10, CompletionTokens: 5, EstimatedCostUSD: .001, LatencyMS: 2, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/admin/model-usage?page_size=10", nil, adminTokens.AccessToken, http.StatusOK, nil)
	requestJSON(t, handler, http.MethodPatch, "/api/v1/admin/users/"+normalTokens.User.ID, map[string]string{"status": "disabled"}, adminTokens.AccessToken, http.StatusOK, nil)
	var active int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id=$1 AND revoked_at IS NULL`, normalTokens.User.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active refresh tokens=%d", active)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "knowflow_ingestion_queue_length") {
		t.Fatalf("metrics status=%d", response.Code)
	}
}
