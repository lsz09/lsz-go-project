//go:build integration

package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each run owns a fresh schema; no existing tables or volumes are reset.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("integration tests require TEST_DATABASE_URL pointing to a dedicated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close admin: %v", err)
		}
	})
	schema := fmt.Sprintf("job_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../../migrations/000001_create_jobs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestPostgresRepository(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	jobs, err := repo.List(ctx, ListOptions{})
	if err != nil || jobs == nil || len(jobs) != 0 {
		t.Fatalf("empty list: %v %v", jobs, err)
	}
	if _, err := repo.FindByID(ctx, "00000000-0000-0000-0000-000000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}

	created, err := repo.Create(ctx, CreateParams{FileName: "access.log"})
	if err != nil {
		t.Fatal(err)
	}
	var uuid pgtype.UUID
	if err := uuid.Scan(created.ID); err != nil || !uuid.Valid {
		t.Fatalf("invalid ID: %q", created.ID)
	}
	if created.Status != StatusPending || created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("unexpected defaults: %+v", created)
	}
	if created.FileKey != nil || created.ResultKey != nil || created.ErrorMessage != nil || created.StartedAt != nil || created.CompletedAt != nil {
		t.Fatalf("NULL values lost: %+v", created)
	}
	found, err := repo.FindByID(ctx, created.ID)
	if err != nil || !reflect.DeepEqual(created, found) {
		t.Fatalf("round trip: %+v %v", found, err)
	}

	empty := ""
	withKey, err := repo.Create(ctx, CreateParams{FileName: "quoted'file.log", FileKey: &empty})
	if err != nil || withKey.FileKey == nil || *withKey.FileKey != "" || withKey.FileName != "quoted'file.log" {
		t.Fatalf("empty key / parameter binding: %+v %v", withKey, err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, "UPDATE jobs SET status = 'COMPLETED', result_key = 'result.json', error_message = '', started_at = $1, completed_at = $1, updated_at = $1 WHERE id = $2", stamp, created.ID); err != nil {
		t.Fatal(err)
	}
	found, err = repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != StatusCompleted || found.ResultKey == nil || *found.ResultKey != "result.json" || found.ErrorMessage == nil || *found.ErrorMessage != "" || found.StartedAt == nil || !found.StartedAt.Equal(stamp) || found.CompletedAt == nil || !found.CompletedAt.Equal(stamp) || !found.UpdatedAt.Equal(stamp) {
		t.Fatalf("non-null mapping: %+v", found)
	}

	// Controlled timestamps and UUIDs verify ordering, including tied timestamps.
	if _, err := pool.Exec(ctx, "DELETE FROM jobs"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		createdAt := stamp
		if i == 1 {
			createdAt = stamp.Add(time.Hour)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO jobs (id, file_name, created_at) VALUES ($1, $2, $3)", id, "access.log", createdAt); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err = repo.List(ctx, ListOptions{})
	if err != nil || len(jobs) != 20 {
		t.Fatalf("default limit: %d %v", len(jobs), err)
	}
	if jobs[0].ID != "00000000-0000-0000-0000-000000000001" || jobs[1].ID != "00000000-0000-0000-0000-000000000025" {
		t.Fatalf("unexpected sorting: %+v", jobs[:2])
	}
	page, err := repo.List(ctx, ListOptions{Limit: 2, Offset: 1})
	if err != nil || len(page) != 2 || page[0].ID != jobs[1].ID || page[1].ID != jobs[2].ID {
		t.Fatalf("pagination: %v %v", page, err)
	}
	all, err := repo.List(ctx, ListOptions{Limit: 100})
	if err != nil || len(all) != 25 {
		t.Fatalf("max limit: %d %v", len(all), err)
	}
	page, err = repo.List(ctx, ListOptions{Offset: 25})
	if err != nil || page == nil || len(page) != 0 {
		t.Fatalf("past end: %v %v", page, err)
	}

	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := repo.Create(canceled, CreateParams{FileName: "canceled.log"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel create: %v", err)
	}
	if _, err := repo.FindByID(canceled, created.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel find: %v", err)
	}
	if _, err := repo.List(canceled, ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel list: %v", err)
	}
}

// TestPostgresRepositoryStatusTransitions는 실제 DB에서 허용 상태와 갱신 컬럼을 검증합니다.
func TestPostgresRepositoryStatusTransitions(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	completedJob, err := repo.Create(ctx, CreateParams{FileName: "completed.log"})
	if err != nil {
		t.Fatalf("create completed test job: %v", err)
	}
	processing, err := repo.MarkProcessing(ctx, completedJob.ID)
	if err != nil {
		t.Fatalf("mark processing: %v", err)
	}
	if processing.Status != StatusProcessing || processing.StartedAt == nil || processing.UpdatedAt.Before(completedJob.UpdatedAt) || processing.CompletedAt != nil || processing.ResultKey != nil || processing.ErrorMessage != nil {
		t.Fatalf("unexpected processing job: %+v", processing)
	}

	completed, err := repo.MarkCompleted(ctx, completedJob.ID, "results/completed.json")
	if err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if completed.Status != StatusCompleted || completed.ResultKey == nil || *completed.ResultKey != "results/completed.json" || completed.CompletedAt == nil || completed.UpdatedAt.Before(processing.UpdatedAt) || completed.ErrorMessage != nil {
		t.Fatalf("unexpected completed job: %+v", completed)
	}
	if _, err := repo.MarkFailed(ctx, completed.ID, "must not overwrite terminal state"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected terminal state conflict, got %v", err)
	}

	failedJob, err := repo.Create(ctx, CreateParams{FileName: "failed.log"})
	if err != nil {
		t.Fatalf("create failed test job: %v", err)
	}
	failedJob, err = repo.MarkProcessing(ctx, failedJob.ID)
	if err != nil {
		t.Fatalf("mark failed job processing: %v", err)
	}
	failed, err := repo.MarkFailed(ctx, failedJob.ID, "processing failed")
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failed.Status != StatusFailed || failed.ErrorMessage == nil || *failed.ErrorMessage != "processing failed" || failed.CompletedAt == nil || failed.UpdatedAt.Before(failedJob.UpdatedAt) || failed.ResultKey != nil {
		t.Fatalf("unexpected failed job: %+v", failed)
	}

	pendingJob, err := repo.Create(ctx, CreateParams{FileName: "pending.log"})
	if err != nil {
		t.Fatalf("create pending test job: %v", err)
	}
	if _, err := repo.MarkCompleted(ctx, pendingJob.ID, "results/pending.json"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected pending completion conflict, got %v", err)
	}
	if _, err := repo.MarkProcessing(ctx, "00000000-0000-0000-0000-000000000999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing job, got %v", err)
	}
}

// TestPostgresRepositoryConcurrentMarkProcessing은 동시에 하나의 Worker만 작업을 획득하는지 검증합니다.
func TestPostgresRepositoryConcurrentMarkProcessing(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := repo.Create(ctx, CreateParams{FileName: "concurrent.log"})
	if err != nil {
		t.Fatalf("create concurrent test job: %v", err)
	}

	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			_, err := repo.MarkProcessing(ctx, created.ID)
			results <- err
		}()
	}
	close(start)

	succeeded := 0
	conflicted := 0
	for i := 0; i < workers; i++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidTransition):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent transition error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got %d and %d", workers-1, succeeded, conflicted)
	}
}
