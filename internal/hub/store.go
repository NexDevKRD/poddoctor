// Package hub implements the PodDoctor fleet hub: a small central service
// that ingests diagnoses posted from many clusters' PodDoctor operators
// (each configured with --webhook-url pointing here) and serves one
// fleet-wide dashboard/API over them, backed by Postgres.
package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// Diagnosis is one stored notification from a cluster's PodDoctor.
type Diagnosis struct {
	ID              int64     `json:"id"`
	Cluster         string    `json:"cluster"`
	Namespace       string    `json:"namespace"`
	Pod             string    `json:"pod"`
	Container       string    `json:"container"`
	RootCause       string    `json:"rootCause"`
	Confidence      string    `json:"confidence"`
	Summary         string    `json:"summary"`
	Recommendation  string    `json:"recommendation"`
	SuppressedCount int       `json:"suppressedCount"`
	ReceivedAt      time.Time `json:"receivedAt"`
}

// Store is a Postgres-backed store of Diagnosis rows.
//
// ponytail: no integration test runs the actual SQL here (this
// environment has no Postgres to test against) — server.go's handler
// tests cover HTTP behavior against a fake diagnosisStore instead. Add a
// docker-compose/testcontainers-backed test hitting a real Postgres
// before relying on this in anything you can't easily roll back.
type Store struct {
	db *sql.DB
}

// Open connects to dsn, verifies it's reachable, and ensures the schema
// exists.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ponytail: schema managed by one idempotent CREATE-IF-NOT-EXISTS block,
// not a migration framework — fine for an additive single-table schema.
// Move to golang-migrate/goose if the schema needs to evolve with real
// migrations (renames, backfills) later.
func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS diagnoses (
			id               BIGSERIAL PRIMARY KEY,
			cluster          TEXT NOT NULL DEFAULT '',
			namespace        TEXT NOT NULL,
			pod              TEXT NOT NULL,
			container        TEXT NOT NULL,
			root_cause       TEXT NOT NULL,
			confidence       TEXT NOT NULL,
			summary          TEXT NOT NULL,
			recommendation   TEXT NOT NULL,
			suppressed_count INT NOT NULL DEFAULT 0,
			received_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_diagnoses_received_at ON diagnoses (received_at DESC);
		CREATE INDEX IF NOT EXISTS idx_diagnoses_cluster_ns ON diagnoses (cluster, namespace);
	`)
	return err
}

// Insert records one diagnosis.
func (s *Store) Insert(ctx context.Context, d Diagnosis) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO diagnoses (cluster, namespace, pod, container, root_cause, confidence, summary, recommendation, suppressed_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, d.Cluster, d.Namespace, d.Pod, d.Container, d.RootCause, d.Confidence, d.Summary, d.Recommendation, d.SuppressedCount)
	return err
}

// Filter narrows List results. Zero values mean "don't filter on this field".
type Filter struct {
	Cluster   string
	Namespace string
	RootCause string
	Limit     int
}

const defaultListLimit = 200
const maxListLimit = 500

// List returns the most recent diagnoses matching f, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Diagnosis, error) {
	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}

	query := `SELECT id, cluster, namespace, pod, container, root_cause, confidence, summary, recommendation, suppressed_count, received_at FROM diagnoses WHERE 1=1`
	var args []any
	if f.Cluster != "" {
		args = append(args, f.Cluster)
		query += fmt.Sprintf(" AND cluster = $%d", len(args))
	}
	if f.Namespace != "" {
		args = append(args, f.Namespace)
		query += fmt.Sprintf(" AND namespace = $%d", len(args))
	}
	if f.RootCause != "" {
		args = append(args, f.RootCause)
		query += fmt.Sprintf(" AND root_cause = $%d", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY received_at DESC LIMIT $%d", len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Diagnosis
	for rows.Next() {
		var d Diagnosis
		if err := rows.Scan(&d.ID, &d.Cluster, &d.Namespace, &d.Pod, &d.Container, &d.RootCause,
			&d.Confidence, &d.Summary, &d.Recommendation, &d.SuppressedCount, &d.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
