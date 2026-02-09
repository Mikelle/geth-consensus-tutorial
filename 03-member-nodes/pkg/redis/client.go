package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps Redis operations for consensus
type Client struct {
	rdb        *redis.Client
	streamName string
}

// NewClient creates a new Redis client
func NewClient(addr, password, streamName string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Client{
		rdb:        rdb,
		streamName: streamName,
	}, nil
}

// PublishBlock publishes a block to the stream
func (c *Client) PublishBlock(ctx context.Context, blockHash, payloadData string, blockNumber uint64) error {
	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.streamName,
		Values: map[string]interface{}{
			"block_hash":   blockHash,
			"block_number": blockNumber,
			"payload":      payloadData,
			"timestamp":    time.Now().UnixMilli(),
		},
	}).Err()
}

// RenewIfValue atomically renews a key's TTL only if it holds the given value
func (c *Client) RenewIfValue(ctx context.Context, key, value string, expiration time.Duration) (bool, error) {
	script := redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	redis.call("set", KEYS[1], ARGV[1], "PX", ARGV[2])
	return 1
end
return 0`)
	result, err := script.Run(ctx, c.rdb, []string{key}, value, expiration.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// DelIfValue atomically deletes a key only if it holds the given value
func (c *Client) DelIfValue(ctx context.Context, key, value string) (bool, error) {
	script := redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0`)
	result, err := script.Run(ctx, c.rdb, []string{key}, value).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// Set stores a value with optional expiration
func (c *Client) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	return c.rdb.Set(ctx, key, value, expiration).Err()
}

// Get retrieves a value
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

// SetNX sets a value only if it doesn't exist (for locking)
func (c *Client) SetNX(ctx context.Context, key, value string, expiration time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, key, value, expiration).Result()
}

// Del deletes a key
func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

// Close closes the Redis connection
func (c *Client) Close() error {
	return c.rdb.Close()
}
