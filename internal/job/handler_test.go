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

// stubJobService는 Handler 테스트에서 실제 Service와 DB를 대체합니다.
type stubJobService struct {
	create func(context.Context, CreateParams) (Job, error)
	list   func(context.Context, ListOptions) ([]Job, error)
}

// Create는 테스트가 지정한 Service 결과를 반환합니다.
func (s stubJobService) Create(ctx context.Context, params CreateParams) (Job, error) {
	return s.create(ctx, params)
}

// List는 테스트가 지정한 Service 조회 결과를 반환합니다.
func (s stubJobService) List(ctx context.Context, options ListOptions) ([]Job, error) {
	return s.list(ctx, options)
}

// TestCreateJobHandler는 정상 요청이 201과 생성 결과를 반환하는지 검증합니다.
func TestCreateJobHandler(t *testing.T) {
	createdAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	service := stubJobService{create: func(ctx context.Context, params CreateParams) (Job, error) {
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
	service := stubJobService{create: func(_ context.Context, params CreateParams) (Job, error) {
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
		service := stubJobService{create: func(context.Context, CreateParams) (Job, error) {
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
	service := stubJobService{create: func(context.Context, CreateParams) (Job, error) {
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
			service := stubJobService{create: func(context.Context, CreateParams) (Job, error) {
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

// TestJobHandlerRejectsUnsupportedMethod는 미지원 요청의 405와 Allow 헤더를 검증합니다.
func TestJobHandlerRejectsUnsupportedMethod(t *testing.T) {
	service := stubJobService{create: func(context.Context, CreateParams) (Job, error) {
		t.Fatal("service must not be called")
		return Job{}, nil
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/jobs", nil)
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("expected Allow GET, POST, got %q", recorder.Header().Get("Allow"))
	}
}

// TestListJobsHandler는 기본 페이지 옵션과 전체 작업 필드 응답을 검증합니다.
func TestListJobsHandler(t *testing.T) {
	createdAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	startedAt := createdAt.Add(10 * time.Second)
	completedAt := createdAt.Add(50 * time.Second)
	fileKey := "uploads/access.log"
	resultKey := "results/access.json"
	service := stubJobService{list: func(ctx context.Context, options ListOptions) ([]Job, error) {
		if ctx == nil || options != (ListOptions{Limit: DefaultListLimit, Offset: 0}) {
			t.Fatalf("unexpected list input: %+v", options)
		}
		return []Job{{
			ID:          "00000000-0000-0000-0000-000000000001",
			Status:      StatusCompleted,
			FileName:    "access.log",
			FileKey:     &fileKey,
			ResultKey:   &resultKey,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
		}}, nil
	}}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	recorder := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Content-Type %q", recorder.Header().Get("Content-Type"))
	}

	var response listJobsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Limit != DefaultListLimit || response.Offset != 0 || len(response.Jobs) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	got := response.Jobs[0]
	if got.ID == "" || got.Status != StatusCompleted || got.FileKey == nil || *got.FileKey != fileKey || got.ResultKey == nil || *got.ResultKey != resultKey || got.ErrorMessage != nil || !got.CreatedAt.Equal(createdAt) || !got.UpdatedAt.Equal(updatedAt) || got.StartedAt == nil || !got.StartedAt.Equal(startedAt) || got.CompletedAt == nil || !got.CompletedAt.Equal(completedAt) {
		t.Fatalf("unexpected job response: %+v", got)
	}
}

// TestListJobsHandlerAppliesPagination은 사용자 지정 limit과 offset 전달을 검증합니다.
func TestListJobsHandlerAppliesPagination(t *testing.T) {
	service := stubJobService{list: func(_ context.Context, options ListOptions) ([]Job, error) {
		if options != (ListOptions{Limit: 2, Offset: 1}) {
			t.Fatalf("unexpected options: %+v", options)
		}
		return []Job{}, nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?limit=2&offset=1", nil)
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var response listJobsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Jobs == nil || len(response.Jobs) != 0 || response.Limit != 2 || response.Offset != 1 {
		t.Fatalf("expected empty page, got %+v", response)
	}
}

// TestListJobsHandlerNormalizesZeroLimit은 limit 0을 기본값으로 반환하는지 검증합니다.
func TestListJobsHandlerNormalizesZeroLimit(t *testing.T) {
	service := stubJobService{list: func(_ context.Context, options ListOptions) ([]Job, error) {
		if options.Limit != DefaultListLimit {
			t.Fatalf("expected default limit, got %+v", options)
		}
		return nil, nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?limit=0", nil)
	recorder := httptest.NewRecorder()

	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var response listJobsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Jobs == nil || response.Limit != DefaultListLimit {
		t.Fatalf("unexpected response: %+v", response)
	}
}

// TestListJobsHandlerRejectsInvalidQuery는 잘못된 페이지 쿼리를 Service 호출 전에 거부합니다.
func TestListJobsHandlerRejectsInvalidQuery(t *testing.T) {
	queries := []string{
		"limit=-1",
		"limit=101",
		"limit=abc",
		"limit=",
		"offset=-1",
		"offset=abc",
		"offset=",
		"limit=1&limit=2",
		"limit=1;offset=2",
		"unknown=value",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			service := stubJobService{list: func(context.Context, ListOptions) ([]Job, error) {
				t.Fatal("service must not be called")
				return nil, nil
			}}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?"+query, nil)
			recorder := httptest.NewRecorder()

			NewHandler(service).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
		})
	}
}

// TestListJobsHandlerMapsServiceErrors는 조회 오류를 안전한 HTTP 응답으로 변환합니다.
func TestListJobsHandlerMapsServiceErrors(t *testing.T) {
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
			service := stubJobService{list: func(context.Context, ListOptions) ([]Job, error) {
				return nil, test.cause
			}}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
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
