package governance

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/config"
)

func TestLimiterEnforcesIPUserAndLoginFailureDimensions(t *testing.T) {
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewLimiter(client, config.Governance{IPRequestsPerMinute: 2, UserRequestsPerMinute: 1, LoginFailures: 2, LoginFailureWindow: time.Minute})
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		allowed, _, err := limiter.AllowIP(ctx, "127.0.0.1")
		if err != nil || !allowed {
			t.Fatalf("attempt %d allowed=%v err=%v", attempt, allowed, err)
		}
	}
	if allowed, _, _ := limiter.AllowIP(ctx, "127.0.0.1"); allowed {
		t.Fatal("third IP request should be limited")
	}
	if allowed, _, _ := limiter.AllowUser(ctx, "user-1"); !allowed {
		t.Fatal("first user request should pass")
	}
	if allowed, _, _ := limiter.AllowUser(ctx, "user-1"); allowed {
		t.Fatal("second user request should be limited")
	}
	if err := limiter.RecordLoginFailure(ctx, "127.0.0.2", "user@example.test"); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := limiter.LoginBlocked(ctx, "127.0.0.2", "user@example.test")
	if err != nil || blocked {
		t.Fatalf("blocked too early: %v %v", blocked, err)
	}
	if err := limiter.RecordLoginFailure(ctx, "127.0.0.2", "user@example.test"); err != nil {
		t.Fatal(err)
	}
	blocked, _, err = limiter.LoginBlocked(ctx, "127.0.0.2", "user@example.test")
	if err != nil || !blocked {
		t.Fatalf("login should be blocked: %v %v", blocked, err)
	}
	if err := limiter.ResetLogin(ctx, "127.0.0.2", "user@example.test"); err != nil {
		t.Fatal(err)
	}
	blocked, _, _ = limiter.LoginBlocked(ctx, "127.0.0.2", "user@example.test")
	if blocked {
		t.Fatal("reset did not clear login failures")
	}
}
