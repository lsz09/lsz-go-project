package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("job not found")
	ErrInvalidInput = errors.New("invalid job input")
)

const (
	DefaultListLimit = 20
	MaxListLimit     = 100
	queryTimeout     = 5 * time.Second
	jobColumns       = "id, status, file_name, file_key, result_key, error_message, created_at, updated_at, started_at, completed_at"
)

// database is the small pgx surface needed by the repository and its tests.
type database interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Repository struct {
	db database
}

// NewRepository reuses the application's pool; the caller retains ownership.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{db: pool}
}

func (r *Repository) Create(ctx context.Context, params CreateParams) (Job, error) {
	if strings.TrimSpace(params.FileName) == "" {
		return Job{}, fmt.Errorf("create job: %w: file name is required", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	result, err := scanJob(r.db.QueryRow(ctx,
		"INSERT INTO jobs (file_name, file_key) VALUES ($1, $2) RETURNING "+jobColumns,
		params.FileName, params.FileKey))
	if err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}
	return result, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (Job, error) {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return Job{}, fmt.Errorf("find job: %w: id must be a hyphenated UUID", ErrInvalidInput)
	}
	var uuid pgtype.UUID
	if err := uuid.Scan(id); err != nil || !uuid.Valid {
		return Job{}, fmt.Errorf("find job: %w: id must be a UUID", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	result, err := scanJob(r.db.QueryRow(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = $1", uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, fmt.Errorf("find job: %w", ErrNotFound)
	}
	if err != nil {
		return Job{}, fmt.Errorf("find job: %w", err)
	}
	return result, nil
}

func (r *Repository) List(ctx context.Context, options ListOptions) ([]Job, error) {
	if options.Limit < 0 || options.Limit > MaxListLimit || options.Offset < 0 {
		return nil, fmt.Errorf("list jobs: %w: limit must be 0..%d and offset must be nonnegative", ErrInvalidInput, MaxListLimit)
	}
	if options.Limit == 0 {
		options.Limit = DefaultListLimit
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := r.db.Query(ctx,
		"SELECT "+jobColumns+" FROM jobs ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2",
		options.Limit, options.Offset)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]Job, 0)
	for rows.Next() {
		result, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs: scan row: %w", err)
		}
		jobs = append(jobs, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: iterate rows: %w", err)
	}
	return jobs, nil
}

func scanJob(row pgx.Row) (Job, error) {
	var result Job
	var id pgtype.UUID
	err := row.Scan(&id, &result.Status, &result.FileName, &result.FileKey,
		&result.ResultKey, &result.ErrorMessage, &result.CreatedAt, &result.UpdatedAt,
		&result.StartedAt, &result.CompletedAt)
	if err != nil {
		return Job{}, err
	}
	result.ID = id.String()
	return result, nil
}
