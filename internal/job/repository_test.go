package job

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeDB struct {
	queryRow func(context.Context, string, ...any) pgx.Row
	query    func(context.Context, string, ...any) (pgx.Rows, error)
}

func (f fakeDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(ctx, sql, args...)
}

func (f fakeDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(ctx, sql, args...)
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

// jobRow는 상태 변경 성공 시 scanJob이 기대하는 전체 컬럼을 채웁니다.
type jobRow struct{ job Job }

func (r jobRow) Scan(destinations ...any) error {
	if len(destinations) != 10 {
		return fmt.Errorf("expected 10 destinations, got %d", len(destinations))
	}
	id := destinations[0].(*pgtype.UUID)
	if err := id.Scan(r.job.ID); err != nil {
		return err
	}
	*destinations[1].(*Status) = r.job.Status
	*destinations[2].(*string) = r.job.FileName
	*destinations[3].(**string) = r.job.FileKey
	*destinations[4].(**string) = r.job.ResultKey
	*destinations[5].(**string) = r.job.ErrorMessage
	*destinations[6].(*time.Time) = r.job.CreatedAt
	*destinations[7].(*time.Time) = r.job.UpdatedAt
	*destinations[8].(**time.Time) = r.job.StartedAt
	*destinations[9].(**time.Time) = r.job.CompletedAt
	return nil
}

// statusRow는 조건부 UPDATE가 실패한 뒤 현재 상태 조회 결과를 반환합니다.
type statusRow struct{ status Status }

func (r statusRow) Scan(destinations ...any) error {
	*destinations[0].(*Status) = r.status
	return nil
}

// Embedding keeps unused pgx methods outside this focused test double.
type fakeRows struct {
	pgx.Rows
	remaining int
	scanErr   error
	finalErr  error
	closed    bool
}

func (r *fakeRows) Next() bool {
	if r.remaining == 0 {
		return false
	}
	r.remaining--
	return true
}
func (r *fakeRows) Scan(...any) error { return r.scanErr }
func (r *fakeRows) Err() error        { return r.finalErr }
func (r *fakeRows) Close()            { r.closed = true }

func TestInvalidInputDoesNotCallDatabase(t *testing.T) {
	repo := &Repository{} // A DB call would panic.
	for _, name := range []string{"", " \t\n"} {
		if _, err := repo.Create(context.Background(), CreateParams{FileName: name}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%q): %v", name, err)
		}
	}
	for _, id := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-00000000000x", "00000000x0000x0000x0000x000000000001"} {
		if _, err := repo.FindByID(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("FindByID(%q): %v", id, err)
		}
		if _, err := repo.MarkProcessing(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("MarkProcessing(%q): %v", id, err)
		}
	}
	if _, err := repo.MarkCompleted(context.Background(), "00000000-0000-0000-0000-000000000001", " \t"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("MarkCompleted empty result key: %v", err)
	}
	if _, err := repo.MarkFailed(context.Background(), "00000000-0000-0000-0000-000000000001", "\n"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("MarkFailed empty error message: %v", err)
	}
	for _, options := range []ListOptions{{Limit: -1}, {Limit: 101}, {Offset: -1}} {
		if _, err := repo.List(context.Background(), options); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("List(%+v): %v", options, err)
		}
	}
}

func TestRepositoryPreservesErrors(t *testing.T) {
	dbError := errors.New("database unavailable")
	for _, cause := range []error{dbError, context.Canceled, context.DeadlineExceeded} {
		repo := &Repository{db: fakeDB{
			queryRow: func(context.Context, string, ...any) pgx.Row { return errorRow{cause} },
			query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, cause },
		}}
		_, createErr := repo.Create(context.Background(), CreateParams{FileName: "access.log"})
		_, findErr := repo.FindByID(context.Background(), "00000000-0000-0000-0000-000000000001")
		_, listErr := repo.List(context.Background(), ListOptions{})
		for _, err := range []error{createErr, findErr, listErr} {
			if !errors.Is(err, cause) || err == cause || errors.Is(err, ErrNotFound) {
				t.Errorf("expected wrapped cause %v, got %v", cause, err)
			}
		}
	}
	repo := &Repository{db: fakeDB{queryRow: func(context.Context, string, ...any) pgx.Row {
		return errorRow{pgx.ErrNoRows}
	}}}
	if _, err := repo.FindByID(context.Background(), "00000000-0000-0000-0000-000000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListOptionsAndRowErrors(t *testing.T) {
	for _, options := range []ListOptions{{}, {Limit: 1, Offset: 2}, {Limit: 100}} {
		rows := &fakeRows{}
		repo := &Repository{db: fakeDB{query: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
			wantLimit := options.Limit
			if wantLimit == 0 {
				wantLimit = 20
			}
			if args[0] != wantLimit || args[1] != options.Offset {
				t.Fatalf("unexpected pagination arguments: %v", args)
			}
			return rows, nil
		}}}
		jobs, err := repo.List(context.Background(), options)
		if err != nil || jobs == nil || len(jobs) != 0 || !rows.closed {
			t.Fatalf("expected empty non-nil list and closed rows: %v, %v", jobs, err)
		}
	}
	cause := errors.New("row failure")
	for _, rows := range []*fakeRows{{remaining: 1, scanErr: cause}, {finalErr: cause}} {
		repo := &Repository{db: fakeDB{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return rows, nil
		}}}
		jobs, err := repo.List(context.Background(), ListOptions{})
		if !errors.Is(err, cause) || jobs != nil || !rows.closed {
			t.Fatalf("expected error and closed rows: %v, %v", jobs, err)
		}
	}
}

// TestRepositoryStatusTransitions는 조건부 UPDATE 인수와 반환 Job을 검증합니다.
func TestRepositoryStatusTransitions(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	resultKey := "results/access.json"
	errorMessage := "processing failed"
	startedAt := now.Add(-time.Minute)

	tests := []struct {
		name          string
		operation     func(*Repository) (Job, error)
		want          Job
		wantValues    []any
		wantFragments []string
	}{
		{
			name: "pending to processing",
			operation: func(repo *Repository) (Job, error) {
				return repo.MarkProcessing(context.Background(), id)
			},
			want:          Job{ID: id, Status: StatusProcessing, FileName: "access.log", CreatedAt: now, UpdatedAt: now, StartedAt: &now},
			wantValues:    []any{StatusProcessing, StatusPending},
			wantFragments: []string{"started_at = CURRENT_TIMESTAMP", "completed_at = NULL", "AND status = $3"},
		},
		{
			name: "processing to completed",
			operation: func(repo *Repository) (Job, error) {
				return repo.MarkCompleted(context.Background(), id, resultKey)
			},
			want:          Job{ID: id, Status: StatusCompleted, FileName: "access.log", ResultKey: &resultKey, CreatedAt: now, UpdatedAt: now, StartedAt: &startedAt, CompletedAt: &now},
			wantValues:    []any{StatusCompleted, resultKey, StatusProcessing},
			wantFragments: []string{"result_key = $3", "error_message = NULL", "AND status = $4"},
		},
		{
			name: "processing to failed",
			operation: func(repo *Repository) (Job, error) {
				return repo.MarkFailed(context.Background(), id, errorMessage)
			},
			want:          Job{ID: id, Status: StatusFailed, FileName: "access.log", ErrorMessage: &errorMessage, CreatedAt: now, UpdatedAt: now, StartedAt: &startedAt, CompletedAt: &now},
			wantValues:    []any{StatusFailed, errorMessage, StatusProcessing},
			wantFragments: []string{"error_message = $3", "result_key = NULL", "AND status = $4"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &Repository{db: fakeDB{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
				if !strings.HasPrefix(query, "UPDATE jobs SET") {
					t.Fatalf("expected UPDATE query, got %q", query)
				}
				for _, fragment := range test.wantFragments {
					if !strings.Contains(query, fragment) {
						t.Fatalf("query does not contain %q: %s", fragment, query)
					}
				}
				if len(args) != len(test.wantValues)+1 {
					t.Fatalf("unexpected arguments: %v", args)
				}
				uuid, ok := args[0].(pgtype.UUID)
				if !ok || uuid.String() != id {
					t.Fatalf("unexpected UUID argument: %v", args[0])
				}
				if !reflect.DeepEqual(args[1:], test.wantValues) {
					t.Fatalf("expected values %v, got %v", test.wantValues, args[1:])
				}
				return jobRow{job: test.want}
			}}}

			got, err := test.operation(repo)
			if err != nil {
				t.Fatalf("transition job: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("expected %+v, got %+v", test.want, got)
			}
		})
	}
}

// TestRepositoryStatusTransitionErrors는 대상 없음, 상태 충돌과 DB 오류를 구분합니다.
func TestRepositoryStatusTransitionErrors(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	dbError := errors.New("database unavailable")
	tests := []struct {
		name        string
		first       pgx.Row
		second      pgx.Row
		want        error
		wantQueries int
	}{
		{name: "update failure", first: errorRow{dbError}, want: dbError, wantQueries: 1},
		{name: "missing job", first: errorRow{pgx.ErrNoRows}, second: errorRow{pgx.ErrNoRows}, want: ErrNotFound, wantQueries: 2},
		{name: "invalid transition", first: errorRow{pgx.ErrNoRows}, second: statusRow{StatusCompleted}, want: ErrInvalidTransition, wantQueries: 2},
		{name: "classification failure", first: errorRow{pgx.ErrNoRows}, second: errorRow{dbError}, want: dbError, wantQueries: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := 0
			repo := &Repository{db: fakeDB{queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
				queries++
				if queries == 1 {
					if !strings.HasPrefix(query, "UPDATE jobs SET") {
						t.Fatalf("expected UPDATE query, got %q", query)
					}
					return test.first
				}
				if query != "SELECT status FROM jobs WHERE id = $1" {
					t.Fatalf("unexpected classification query %q", query)
				}
				return test.second
			}}}

			_, err := repo.MarkProcessing(context.Background(), id)
			if !errors.Is(err, test.want) || err == test.want {
				t.Fatalf("expected wrapped %v, got %v", test.want, err)
			}
			if queries != test.wantQueries {
				t.Fatalf("expected %d queries, got %d", test.wantQueries, queries)
			}
		})
	}
}

func TestContextPropagation(t *testing.T) {
	operations := map[string]func(*Repository, context.Context) error{
		"create": func(r *Repository, ctx context.Context) error {
			_, err := r.Create(ctx, CreateParams{FileName: "access.log"})
			return err
		},
		"find": func(r *Repository, ctx context.Context) error {
			_, err := r.FindByID(ctx, "00000000-0000-0000-0000-000000000001")
			return err
		},
		"list": func(r *Repository, ctx context.Context) error {
			_, err := r.List(ctx, ListOptions{})
			return err
		},
		"mark processing": func(r *Repository, ctx context.Context) error {
			_, err := r.MarkProcessing(ctx, "00000000-0000-0000-0000-000000000001")
			return err
		},
		"mark completed": func(r *Repository, ctx context.Context) error {
			_, err := r.MarkCompleted(ctx, "00000000-0000-0000-0000-000000000001", "results/access.json")
			return err
		},
		"mark failed": func(r *Repository, ctx context.Context) error {
			_, err := r.MarkFailed(ctx, "00000000-0000-0000-0000-000000000001", "processing failed")
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"default", "short", "canceled", "expired"} {
				t.Run(mode, func(t *testing.T) {
					parent := context.Background()
					cancel := func() {}
					switch mode {
					case "short":
						parent, cancel = context.WithTimeout(parent, time.Second)
					case "canceled":
						parent, cancel = context.WithCancel(parent)
						cancel()
					case "expired":
						parent, cancel = context.WithDeadline(parent, time.Now().Add(-time.Second))
					}
					defer cancel()
					var captured context.Context
					check := func(ctx context.Context) error {
						captured = ctx
						deadline, ok := ctx.Deadline()
						if !ok || time.Until(deadline) > 5*time.Second {
							t.Fatal("missing repository timeout")
						}
						if want, ok := parent.Deadline(); ok && !deadline.Equal(want) {
							t.Fatal("parent deadline was changed")
						}
						return ctx.Err()
					}
					repo := &Repository{db: fakeDB{
						queryRow: func(ctx context.Context, _ string, _ ...any) pgx.Row {
							err := check(ctx)
							if err == nil {
								err = pgx.ErrNoRows
							}
							return errorRow{err}
						},
						query: func(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
							if err := check(ctx); err != nil {
								return nil, err
							}
							return &fakeRows{}, nil
						},
					}}
					err := operation(repo, parent)
					if parent.Err() != nil && !errors.Is(err, parent.Err()) {
						t.Fatalf("lost context error: %v", err)
					}
					if captured == nil || captured.Err() == nil {
						t.Fatal("query context was not canceled after return")
					}
				})
			}
		})
	}
}
