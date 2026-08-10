package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/config"
)

type Limiter struct {
	client redisclient.Cmdable
	config config.Governance
}

func NewLimiter(client redisclient.Cmdable, cfg config.Governance) *Limiter {
	return &Limiter{client: client, config: cfg}
}

func (l *Limiter) AllowIP(ctx context.Context, ip string) (bool, time.Duration, error) {
	return l.allow(ctx, "ip:"+digest(ip), l.config.IPRequestsPerMinute, time.Minute, true)
}

func (l *Limiter) AllowUser(ctx context.Context, userID string) (bool, time.Duration, error) {
	return l.allow(ctx, "user:"+digest(userID), l.config.UserRequestsPerMinute, time.Minute, true)
}

func (l *Limiter) LoginBlocked(ctx context.Context, ip, email string) (bool, time.Duration, error) {
	key := "login:" + digest(ip+"\x00"+email)
	count, err := l.client.Get(ctx, "knowflow:limit:"+key).Int()
	if err != nil && err != redisclient.Nil {
		return false, 0, err
	}
	ttl, _ := l.client.TTL(ctx, "knowflow:limit:"+key).Result()
	return count >= l.config.LoginFailures, max(ttl, 0), nil
}

func (l *Limiter) RecordLoginFailure(ctx context.Context, ip, email string) error {
	_, _, err := l.allow(ctx, "login:"+digest(ip+"\x00"+email), l.config.LoginFailures, l.config.LoginFailureWindow, true)
	return err
}

func (l *Limiter) ResetLogin(ctx context.Context, ip, email string) error {
	return l.client.Del(ctx, "knowflow:limit:login:"+digest(ip+"\x00"+email)).Err()
}

func (l *Limiter) allow(ctx context.Context, key string, limit int, window time.Duration, increment bool) (bool, time.Duration, error) {
	if !increment {
		return true, 0, nil
	}
	const script = `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; local ttl=redis.call('PTTL',KEYS[1]); return {n,ttl}`
	values, err := l.client.Eval(ctx, script, []string{"knowflow:limit:" + key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("rate limiter: %w", err)
	}
	if len(values) != 2 {
		return false, 0, fmt.Errorf("rate limiter returned invalid result")
	}
	return values[0] <= int64(limit), time.Duration(max(values[1], int64(0))) * time.Millisecond, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}
