package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PayloadStore handles payload persistence in PostgreSQL
type PayloadStore struct {
	pool *pgxpool.Pool
}

// NewPayloadStore creates a new PostgreSQL payload store
func NewPayloadStore(ctx context.Context, connString string) (*PayloadStore, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}

	config.MaxConns = 10
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	store := &PayloadStore{pool: pool}
	if err := store.migrate(ctx); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return store, nil
}

func (s *PayloadStore) migrate(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS payloads (
			block_number BIGINT PRIMARY KEY,
			block_hash TEXT NOT NULL UNIQUE,
			parent_hash TEXT NOT NULL,
			payload_data TEXT NOT NULL,
			requests_data TEXT NOT NULL DEFAULT '',
			timestamp BIGINT NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_payloads_block_hash ON payloads(block_hash);
		CREATE INDEX IF NOT EXISTS idx_payloads_timestamp ON payloads(timestamp);

		ALTER TABLE payloads ADD COLUMN IF NOT EXISTS requests_data TEXT NOT NULL DEFAULT '';
	`
	_, err := s.pool.Exec(ctx, query)
	return err
}

// Payload represents a stored execution payload
type Payload struct {
	BlockNumber  uint64
	BlockHash    string
	ParentHash   string
	PayloadData  string
	RequestsData string
	Timestamp    int64
}

// SavePayload stores a payload
func (s *PayloadStore) SavePayload(ctx context.Context, p *Payload) error {
	query := `
		INSERT INTO payloads (block_number, block_hash, parent_hash, payload_data, requests_data, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (block_number) DO UPDATE SET
			block_hash = EXCLUDED.block_hash,
			parent_hash = EXCLUDED.parent_hash,
			payload_data = EXCLUDED.payload_data,
			requests_data = EXCLUDED.requests_data,
			timestamp = EXCLUDED.timestamp
	`
	_, err := s.pool.Exec(ctx, query, p.BlockNumber, p.BlockHash, p.ParentHash, p.PayloadData, p.RequestsData, p.Timestamp)
	return err
}

// GetPayloadByNumber retrieves a payload by block number
func (s *PayloadStore) GetPayloadByNumber(ctx context.Context, number uint64) (*Payload, error) {
	query := `SELECT block_number, block_hash, parent_hash, payload_data, requests_data, timestamp FROM payloads WHERE block_number = $1`
	row := s.pool.QueryRow(ctx, query, number)

	var p Payload
	if err := row.Scan(&p.BlockNumber, &p.BlockHash, &p.ParentHash, &p.PayloadData, &p.RequestsData, &p.Timestamp); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPayloadByHash retrieves a payload by block hash
func (s *PayloadStore) GetPayloadByHash(ctx context.Context, hash string) (*Payload, error) {
	query := `SELECT block_number, block_hash, parent_hash, payload_data, requests_data, timestamp FROM payloads WHERE block_hash = $1`
	row := s.pool.QueryRow(ctx, query, hash)

	var p Payload
	if err := row.Scan(&p.BlockNumber, &p.BlockHash, &p.ParentHash, &p.PayloadData, &p.RequestsData, &p.Timestamp); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetLatestPayload retrieves the most recent payload
func (s *PayloadStore) GetLatestPayload(ctx context.Context) (*Payload, error) {
	query := `SELECT block_number, block_hash, parent_hash, payload_data, requests_data, timestamp FROM payloads ORDER BY block_number DESC LIMIT 1`
	row := s.pool.QueryRow(ctx, query)

	var p Payload
	if err := row.Scan(&p.BlockNumber, &p.BlockHash, &p.ParentHash, &p.PayloadData, &p.RequestsData, &p.Timestamp); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPayloadsAfter retrieves payloads after a given block number
func (s *PayloadStore) GetPayloadsAfter(ctx context.Context, afterNumber uint64, limit int) ([]*Payload, error) {
	query := `
		SELECT block_number, block_hash, parent_hash, payload_data, requests_data, timestamp
		FROM payloads
		WHERE block_number > $1
		ORDER BY block_number ASC
		LIMIT $2
	`
	rows, err := s.pool.Query(ctx, query, afterNumber, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payloads []*Payload
	for rows.Next() {
		var p Payload
		if err := rows.Scan(&p.BlockNumber, &p.BlockHash, &p.ParentHash, &p.PayloadData, &p.RequestsData, &p.Timestamp); err != nil {
			return nil, err
		}
		payloads = append(payloads, &p)
	}

	return payloads, rows.Err()
}

// Close closes the database connection pool
func (s *PayloadStore) Close() {
	s.pool.Close()
}
