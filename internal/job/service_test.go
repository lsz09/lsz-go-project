package job

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// stubServiceRepository는 Service 테스트에서 실제 PostgreSQL을 대체합니다.
type stubServiceRepository struct {
	create func(context.Context, CreateParams) (Job, error)
	list   func(context.Context, ListOptions) ([]Job, error)
}

// Create는 테스트가 지정한 동작을 실행합니다.
func (s stubServiceRepository) Create(ctx context.Context, params CreateParams) (Job, error) {
	return s.create(ctx, params)
}

// List는 테스트가 지정한 조회 동작을 실행합니다.
func (s stubServiceRepository) List(ctx context.Context, options ListOptions) ([]Job, error) {
	return s.list(ctx, options)
}

// TestServiceCreate는 입력과 Context가 Repository에 전달되는지 검증합니다.
func TestServiceCreate(t *testing.T) {
	createdAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	want := Job{
		ID:        "00000000-0000-0000-0000-000000000001",
		Status:    StatusPending,
		FileName:  "access.log",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-1")

	repository := stubServiceRepository{create: func(gotContext context.Context, params CreateParams) (Job, error) {
		if gotContext.Value(requestIDKey) != "request-1" {
			t.Fatal("caller context was not propagated")
		}
		if params.FileName != "access.log" {
			t.Fatalf("unexpected file name %q", params.FileName)
		}
		return want, nil
	}}

	got, err := NewService(repository).Create(ctx, CreateParams{FileName: "access.log"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

// TestServiceCreateRejectsEmptyFileName은 잘못된 입력이 DB까지 도달하지 않는지 검증합니다.
func TestServiceCreateRejectsEmptyFileName(t *testing.T) {
	repository := stubServiceRepository{create: func(context.Context, CreateParams) (Job, error) {
		t.Fatal("repository must not be called")
		return Job{}, nil
	}}
	service := NewService(repository)

	for _, fileName := range []string{"", " ", "\t\n"} {
		if _, err := service.Create(context.Background(), CreateParams{FileName: fileName}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%q): expected ErrInvalidInput, got %v", fileName, err)
		}
	}
}

// TestServiceCreatePreservesErrors는 Repository와 Context 오류 원인이 유지되는지 검증합니다.
func TestServiceCreatePreservesErrors(t *testing.T) {
	for _, cause := range []error{errors.New("database unavailable"), context.Canceled} {
		repository := stubServiceRepository{create: func(context.Context, CreateParams) (Job, error) {
			return Job{}, cause
		}}

		_, err := NewService(repository).Create(context.Background(), CreateParams{FileName: "access.log"})
		if !errors.Is(err, cause) || err == cause {
			t.Errorf("expected wrapped cause %v, got %v", cause, err)
		}
	}
}

// TestServiceList는 조회 옵션과 Context가 Repository에 그대로 전달되는지 검증합니다.
func TestServiceList(t *testing.T) {
	want := []Job{{ID: "00000000-0000-0000-0000-000000000001", Status: StatusPending, FileName: "access.log"}}
	options := ListOptions{Limit: 10, Offset: 5}

	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-2")

	repository := stubServiceRepository{list: func(gotContext context.Context, gotOptions ListOptions) ([]Job, error) {
		if gotContext.Value(requestIDKey) != "request-2" {
			t.Fatal("caller context was not propagated")
		}
		if gotOptions != options {
			t.Fatalf("expected options %+v, got %+v", options, gotOptions)
		}
		return want, nil
	}}

	got, err := NewService(repository).List(ctx, options)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

// TestServiceListPreservesErrors는 Repository 조회 오류의 원인을 유지하는지 검증합니다.
func TestServiceListPreservesErrors(t *testing.T) {
	for _, cause := range []error{ErrInvalidInput, errors.New("database unavailable"), context.Canceled} {
		repository := stubServiceRepository{list: func(context.Context, ListOptions) ([]Job, error) {
			return nil, cause
		}}

		_, err := NewService(repository).List(context.Background(), ListOptions{})
		if !errors.Is(err, cause) || err == cause {
			t.Errorf("expected wrapped cause %v, got %v", cause, err)
		}
	}
}

// TestServiceListPreservesEmptySlice는 빈 조회 결과를 nil로 바꾸지 않는지 검증합니다.
func TestServiceListPreservesEmptySlice(t *testing.T) {
	repository := stubServiceRepository{list: func(context.Context, ListOptions) ([]Job, error) {
		return []Job{}, nil
	}}

	jobs, err := NewService(repository).List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if jobs == nil || len(jobs) != 0 {
		t.Fatalf("expected non-nil empty slice, got %#v", jobs)
	}
}
