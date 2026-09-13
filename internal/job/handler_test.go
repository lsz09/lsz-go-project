package job

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubCreateService는 Handler 테스트에서 실제 Service와 DB를 대체합니다.
type stubCreateService struct {
	create func(context.Context, CreateParams) (Job, error)
}

// Create는 테스트가 지정한 Service 결과를 반환합니다.
func (s stubCreateService) Create(ctx context.Context, params CreateParams) (Job, error) {
	return s.create(ctx, params)
}

// TestCreateJobHandler는 정상 요청이 201과 생성 결과를 반환하는지 검증합니다.
func TestCreateJobHandler(t *testing.T) {
	createdAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	service := stubCreateService{create: func(ctx context.Context, params CreateParams) (Job, error) {
		if ctx == nil || params.FileName != "access.log" || params.FileKey != nil {
			t.Fatalf("unexpected create input: %+v", params)
		}
		return Job{
			ID:        "00000000-0000-0000-0000-000000000001",
			Status:    StatusPending,
			FileName:  params.FileName,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}, nil
	}}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"file_name":"access.log"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Content-Type %q", recorder.Header().Get("Content-Type"))
	}

	var response createJobResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID == "" || response.Status != StatusPending || response.FileName != "access.log" || !response.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected response: %+v", response)
	}
}

// TestCreateJobHandlerPassesFileKey는 선택한 저장소 키가 Service까지 전달되는지 검증합니다.
func TestCreateJobHandlerPassesFileKey(t *testing.T) {
	service := stubCreateService{create: func(_ context.Context, params CreateParams) (Job, error) {
		if params.FileKey == nil || *params.FileKey != "uploads/access.log" {
			t.Fatalf("unexpected file key: %v", params.FileKey)
		}
		return Job{
			ID:        "00000000-0000-0000-0000-000000000001",
			Status:    StatusPending,
			FileName:  params.FileName,
			FileKey:   params.FileKey,
			CreatedAt: time.Now().UTC(),
		}, nil
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/jobs",
		strings.NewReader(`{"file_name":"access.log","file_key":"uploads/access.log"}`),
	)
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", recorder.Code)
	}
}

// TestCreateJobHandlerRejectsInvalidJSON은 비정상 JSON이 Service를 호출하지 않는지 검증합니다.
func TestCreateJobHandlerRejectsInvalidJSON(t *testing.T) {
	tests := []string{
		`{"file_name":`,
		`{"file_name":"access.log","unknown":"value"}`,
		`{"file_name":"a.log"}{"file_name":"b.log"}`,
	}

	for _, body := range tests {
		service := stubCreateService{create: func(context.Context, CreateParams) (Job, error) {
			t.Fatal("service must not be called")
			return Job{}, nil
		}}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		NewHandler(service).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, recorder.Code)
		}
	}
}

// TestCreateJobHandlerRejectsOversizedBody는 1MiB를 넘는 요청이 Service에 도달하지 않는지 검증합니다.
func TestCreateJobHandlerRejectsOversizedBody(t *testing.T) {
	service := stubCreateService{create: func(context.Context, CreateParams) (Job, error) {
		t.Fatal("service must not be called")
		return Job{}, nil
	}}
	body := `{"file_name":"` + strings.Repeat("a", int(maxCreateRequestBytes)) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

// TestCreateJobHandlerMapsServiceErrors는 입력 오류와 내부 오류의 HTTP 매핑을 검증합니다.
func TestCreateJobHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		wantCode int
	}{
		{name: "invalid input", cause: ErrInvalidInput, wantCode: http.StatusBadRequest},
		{name: "internal error", cause: errors.New("database password must not be exposed"), wantCode: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := stubCreateService{create: func(context.Context, CreateParams) (Job, error) {
				return Job{}, test.cause
			}}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"file_name":"access.log"}`))
			recorder := httptest.NewRecorder()

			NewHandler(service).ServeHTTP(recorder, request)

			if recorder.Code != test.wantCode {
				t.Fatalf("expected %d, got %d", test.wantCode, recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "database password") {
				t.Fatal("internal error details were exposed")
			}
		})
	}
}

// TestCreateJobHandlerRejectsUnsupportedMethod는 GET 요청에 405와 Allow 헤더를 검증합니다.
func TestCreateJobHandlerRejectsUnsupportedMethod(t *testing.T) {
	service := stubCreateService{create: func(context.Context, CreateParams) (Job, error) {
		t.Fatal("service must not be called")
		return Job{}, nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("expected Allow POST, got %q", recorder.Header().Get("Allow"))
	}
}
