package job

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// stubCreateRepository는 Service 테스트에서 실제 PostgreSQL을 대체합니다.
type stubCreateRepository struct {
	create func(context.Context, CreateParams) (Job, error)
}

// Create는 테스트가 지정한 동작을 실행합니다.
func (s stubCreateRepository) Create(ctx context.Context, params CreateParams) (Job, error) {
	return s.create(ctx, params)
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

	repository := stubCreateRepository{create: func(gotContext context.Context, params CreateParams) (Job, error) {
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
	repository := stubCreateRepository{create: func(context.Context, CreateParams) (Job, error) {
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
		repository := stubCreateRepository{create: func(context.Context, CreateParams) (Job, error) {
			return Job{}, cause
		}}

		_, err := NewService(repository).Create(context.Background(), CreateParams{FileName: "access.log"})
		if !errors.Is(err, cause) || err == cause {
			t.Errorf("expected wrapped cause %v, got %v", cause, err)
		}
	}
}
