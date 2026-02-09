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

// StreamEntry represents a block from the stream
type StreamEntry struct {
	ID          string
	BlockHash   string
	BlockNumber uint64
	Payload     string
	Timestamp   int64
}

// ReadBlocks reads blocks from the stream
func (c *Client) ReadBlocks(ctx context.Context, lastID string, count int64) ([]StreamEntry, error) {
	if lastID == "" {
		lastID = "0"
	}

	streams, err := c.rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{c.streamName, lastID},
		Count:   count,
		Block:   100 * time.Millisecond,
	}).Result()

	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var entries []StreamEntry
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			entry := StreamEntry{ID: msg.ID}
			if v, ok := msg.Values["block_hash"].(string); ok {
				entry.BlockHash = v
			}
			if v, ok := msg.Values["block_number"].(string); ok {
				fmt.Sscanf(v, "%d", &entry.BlockNumber)
			}
			if v, ok := msg.Values["payload"].(string); ok {
				entry.Payload = v
			}
			if v, ok := msg.Values["timestamp"].(string); ok {
				fmt.Sscanf(v, "%d", &entry.Timestamp)
			}
			entries = append(entries, entry)
		}
	}

	return entries, nil
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
