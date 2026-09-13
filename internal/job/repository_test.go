package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
