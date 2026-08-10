package redis

import (
	"context"
	"fmt"

	redisclient "github.com/redis/go-redis/v9"

	"knowflow/internal/config"
)

type Client struct {
	client *redisclient.Client
}

func New(cfg config.Redis) *Client {
	return &Client{client: redisclient.NewClient(&redisclient.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password.Value(),
		DB:       cfg.DB,
	})}
}

func (c *Client) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

func (c *Client) Close() error { return c.client.Close() }
