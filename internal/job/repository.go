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
	ErrNotFound          = errors.New("job not found")
	ErrInvalidInput      = errors.New("invalid job input")
	ErrInvalidTransition = errors.New("invalid job status transition")
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
	uuid, err := parseJobUUID("find job", id)
	if err != nil {
		return Job{}, err
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

// MarkProcessing은 PENDING 작업 하나를 원자적으로 PROCESSING 상태로 변경합니다.
func (r *Repository) MarkProcessing(ctx context.Context, id string) (Job, error) {
	return r.transition(ctx, "mark job processing", id, StatusPending,
		"UPDATE jobs SET status = $2, started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, completed_at = NULL, result_key = NULL, error_message = NULL WHERE id = $1 AND status = $3 RETURNING "+jobColumns,
		StatusProcessing, StatusPending,
	)
}

// MarkCompleted는 PROCESSING 작업에 결과 키를 기록하고 COMPLETED 상태로 변경합니다.
func (r *Repository) MarkCompleted(ctx context.Context, id string, resultKey string) (Job, error) {
	// 처리 결과를 찾을 수 있도록 빈 결과 저장소 키를 DB 호출 전에 거부합니다.
	if strings.TrimSpace(resultKey) == "" {
		return Job{}, fmt.Errorf("mark job completed: %w: result key is required", ErrInvalidInput)
	}

	return r.transition(ctx, "mark job completed", id, StatusProcessing,
		"UPDATE jobs SET status = $2, result_key = $3, completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, error_message = NULL WHERE id = $1 AND status = $4 RETURNING "+jobColumns,
		StatusCompleted, resultKey, StatusProcessing,
	)
}

// MarkFailed는 PROCESSING 작업에 실패 원인을 기록하고 FAILED 상태로 변경합니다.
func (r *Repository) MarkFailed(ctx context.Context, id string, errorMessage string) (Job, error) {
	// 장애 원인을 잃지 않도록 빈 실패 메시지를 DB 호출 전에 거부합니다.
	if strings.TrimSpace(errorMessage) == "" {
		return Job{}, fmt.Errorf("mark job failed: %w: error message is required", ErrInvalidInput)
	}

	return r.transition(ctx, "mark job failed", id, StatusProcessing,
		"UPDATE jobs SET status = $2, error_message = $3, completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, result_key = NULL WHERE id = $1 AND status = $4 RETURNING "+jobColumns,
		StatusFailed, errorMessage, StatusProcessing,
	)
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

// transition은 현재 상태 조건을 포함한 UPDATE로 하나의 Worker만 상태 변경에 성공하게 합니다.
func (r *Repository) transition(ctx context.Context, operation string, id string, expected Status, query string, values ...any) (Job, error) {
	uuid, err := parseJobUUID(operation, id)
	if err != nil {
		return Job{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	args := make([]any, 0, len(values)+1)
	args = append(args, uuid)
	args = append(args, values...)

	updated, err := scanJob(r.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, r.classifyTransitionMiss(ctx, operation, uuid, expected)
	}
	if err != nil {
		return Job{}, fmt.Errorf("%s: %w", operation, err)
	}
	return updated, nil
}

// classifyTransitionMiss는 조건부 UPDATE 실패 후 대상 없음과 상태 충돌만 구분합니다.
func (r *Repository) classifyTransitionMiss(ctx context.Context, operation string, uuid pgtype.UUID, expected Status) error {
	var current Status
	err := r.db.QueryRow(ctx, "SELECT status FROM jobs WHERE id = $1", uuid).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("%s: classify transition: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: current status %s, expected %s", operation, ErrInvalidTransition, current, expected)
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
