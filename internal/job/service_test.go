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
	create         func(context.Context, CreateParams) (Job, error)
	find           func(context.Context, string) (Job, error)
	list           func(context.Context, ListOptions) ([]Job, error)
	markProcessing func(context.Context, string) (Job, error)
	markCompleted  func(context.Context, string, string) (Job, error)
	markFailed     func(context.Context, string, string) (Job, error)
}

// Create는 테스트가 지정한 동작을 실행합니다.
func (s stubServiceRepository) Create(ctx context.Context, params CreateParams) (Job, error) {
	return s.create(ctx, params)
}

// FindByID는 테스트가 지정한 단건 조회 동작을 실행합니다.
func (s stubServiceRepository) FindByID(ctx context.Context, id string) (Job, error) {
	return s.find(ctx, id)
}

// List는 테스트가 지정한 조회 동작을 실행합니다.
func (s stubServiceRepository) List(ctx context.Context, options ListOptions) ([]Job, error) {
	return s.list(ctx, options)
}

// MarkProcessing은 테스트가 지정한 처리 시작 동작을 실행합니다.
func (s stubServiceRepository) MarkProcessing(ctx context.Context, id string) (Job, error) {
	return s.markProcessing(ctx, id)
}

// MarkCompleted는 테스트가 지정한 처리 완료 동작을 실행합니다.
func (s stubServiceRepository) MarkCompleted(ctx context.Context, id string, resultKey string) (Job, error) {
	return s.markCompleted(ctx, id, resultKey)
}

// MarkFailed는 테스트가 지정한 처리 실패 동작을 실행합니다.
func (s stubServiceRepository) MarkFailed(ctx context.Context, id string, errorMessage string) (Job, error) {
	return s.markFailed(ctx, id, errorMessage)
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

// TestServiceFindByID는 작업 ID와 Context가 Repository에 그대로 전달되는지 검증합니다.
func TestServiceFindByID(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	fileKey := "uploads/access.log"
	want := Job{ID: id, Status: StatusPending, FileName: "access.log", FileKey: &fileKey}

	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-3")

	repository := stubServiceRepository{find: func(gotContext context.Context, gotID string) (Job, error) {
		if gotContext.Value(requestIDKey) != "request-3" {
			t.Fatal("caller context was not propagated")
		}
		if gotID != id {
			t.Fatalf("expected id %q, got %q", id, gotID)
		}
		return want, nil
	}}

	got, err := NewService(repository).FindByID(ctx, id)
	if err != nil {
		t.Fatalf("find job: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

// TestServiceFindByIDPreservesErrors는 단건 조회 오류를 감싸면서 원인을 보존하는지 검증합니다.
func TestServiceFindByIDPreservesErrors(t *testing.T) {
	causes := []error{ErrInvalidInput, ErrNotFound, errors.New("database unavailable"), context.Canceled}
	for _, cause := range causes {
		repository := stubServiceRepository{find: func(context.Context, string) (Job, error) {
			return Job{}, cause
		}}

		_, err := NewService(repository).FindByID(context.Background(), "00000000-0000-0000-0000-000000000001")
		if !errors.Is(err, cause) || err == cause {
			t.Errorf("expected wrapped cause %v, got %v", cause, err)
		}
	}
}

// TestServiceStatusTransitions는 Context와 상태별 입력이 Repository에 전달되는지 검증합니다.
func TestServiceStatusTransitions(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	const resultKey = "results/access.json"
	const errorMessage = "processing failed"

	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-4")
	want := Job{ID: id, Status: StatusProcessing, FileName: "access.log"}

	repository := stubServiceRepository{
		markProcessing: func(gotContext context.Context, gotID string) (Job, error) {
			if gotContext.Value(requestIDKey) != "request-4" || gotID != id {
				t.Fatalf("unexpected processing input: %q", gotID)
			}
			return want, nil
		},
		markCompleted: func(gotContext context.Context, gotID string, gotResultKey string) (Job, error) {
			if gotContext.Value(requestIDKey) != "request-4" || gotID != id || gotResultKey != resultKey {
				t.Fatalf("unexpected completed input: %q %q", gotID, gotResultKey)
			}
			completed := want
			completed.Status = StatusCompleted
			return completed, nil
		},
		markFailed: func(gotContext context.Context, gotID string, gotErrorMessage string) (Job, error) {
			if gotContext.Value(requestIDKey) != "request-4" || gotID != id || gotErrorMessage != errorMessage {
				t.Fatalf("unexpected failed input: %q %q", gotID, gotErrorMessage)
			}
			failed := want
			failed.Status = StatusFailed
			return failed, nil
		},
	}
	service := NewService(repository)

	processing, err := service.MarkProcessing(ctx, id)
	if err != nil || processing.Status != StatusProcessing {
		t.Fatalf("mark processing: %+v %v", processing, err)
	}
	completed, err := service.MarkCompleted(ctx, id, resultKey)
	if err != nil || completed.Status != StatusCompleted {
		t.Fatalf("mark completed: %+v %v", completed, err)
	}
	failed, err := service.MarkFailed(ctx, id, errorMessage)
	if err != nil || failed.Status != StatusFailed {
		t.Fatalf("mark failed: %+v %v", failed, err)
	}
}

// TestServiceStatusTransitionValidation은 빈 결과 키와 실패 메시지를 Repository 호출 전에 거부합니다.
func TestServiceStatusTransitionValidation(t *testing.T) {
	repository := stubServiceRepository{
		markCompleted: func(context.Context, string, string) (Job, error) {
			t.Fatal("repository must not be called")
			return Job{}, nil
		},
		markFailed: func(context.Context, string, string) (Job, error) {
			t.Fatal("repository must not be called")
			return Job{}, nil
		},
	}
	service := NewService(repository)

	for _, value := range []string{"", " ", "\t\n"} {
		if _, err := service.MarkCompleted(context.Background(), "id", value); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("MarkCompleted(%q): expected ErrInvalidInput, got %v", value, err)
		}
		if _, err := service.MarkFailed(context.Background(), "id", value); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("MarkFailed(%q): expected ErrInvalidInput, got %v", value, err)
		}
	}
}

// TestServiceStatusTransitionPreservesErrors는 모든 상태 변경에서 Repository 오류 원인을 유지합니다.
func TestServiceStatusTransitionPreservesErrors(t *testing.T) {
	causes := []error{ErrInvalidInput, ErrNotFound, ErrInvalidTransition, errors.New("database unavailable"), context.Canceled}
	for _, cause := range causes {
		repository := stubServiceRepository{
			markProcessing: func(context.Context, string) (Job, error) { return Job{}, cause },
			markCompleted:  func(context.Context, string, string) (Job, error) { return Job{}, cause },
			markFailed:     func(context.Context, string, string) (Job, error) { return Job{}, cause },
		}
		service := NewService(repository)
		operations := []func() (Job, error){
			func() (Job, error) { return service.MarkProcessing(context.Background(), "id") },
			func() (Job, error) { return service.MarkCompleted(context.Background(), "id", "result.json") },
			func() (Job, error) { return service.MarkFailed(context.Background(), "id", "failed") },
		}

		for _, operation := range operations {
			_, err := operation()
			if !errors.Is(err, cause) || err == cause {
				t.Errorf("expected wrapped cause %v, got %v", cause, err)
			}
		}
	}
}
