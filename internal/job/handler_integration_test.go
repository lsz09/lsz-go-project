//go:build integration

package job

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCreateJobHandlerPostgres는 HTTP부터 실제 PostgreSQL 저장까지 전체 생성 흐름을 검증합니다.
func TestCreateJobHandlerPostgres(t *testing.T) {
	pool := integrationPool(t)
	repository := NewRepository(pool)
	handler := NewHandler(NewService(repository))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"file_name":"access.log"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response createJobResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	stored, err := repository.FindByID(context.Background(), response.ID)
	if err != nil {
		t.Fatalf("find created job: %v", err)
	}
	if stored.Status != StatusPending || stored.FileName != "access.log" {
		t.Fatalf("unexpected stored job: %+v", stored)
	}
}

// TestListJobsHandlerPostgres는 HTTP 쿼리부터 PostgreSQL 정렬과 페이지 조회까지 검증합니다.
func TestListJobsHandlerPostgres(t *testing.T) {
	pool := integrationPool(t)
	handler := NewHandler(NewService(NewRepository(pool)))

	// 데이터가 없을 때 jobs를 null이 아닌 빈 배열로 반환하는지 먼저 확인합니다.
	emptyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	emptyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(emptyRecorder, emptyRequest)
	if emptyRecorder.Code != http.StatusOK {
		t.Fatalf("expected empty list 200, got %d: %s", emptyRecorder.Code, emptyRecorder.Body.String())
	}
	var emptyResponse listJobsResponse
	if err := json.NewDecoder(emptyRecorder.Body).Decode(&emptyResponse); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if emptyResponse.Jobs == nil || len(emptyResponse.Jobs) != 0 {
		t.Fatalf("expected non-nil empty jobs, got %#v", emptyResponse.Jobs)
	}

	// 생성 시각과 UUID를 고정해 동률 정렬까지 예측 가능한 목록을 준비합니다.
	ctx := context.Background()
	base := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id        string
		createdAt time.Time
	}{
		{id: "00000000-0000-0000-0000-000000000001", createdAt: base.Add(time.Hour)},
		{id: "00000000-0000-0000-0000-000000000002", createdAt: base},
		{id: "00000000-0000-0000-0000-000000000003", createdAt: base},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, "INSERT INTO jobs (id, file_name, created_at, updated_at) VALUES ($1, $2, $3, $3)", row.id, "access.log", row.createdAt); err != nil {
			t.Fatalf("insert job %s: %v", row.id, err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?limit=2&offset=1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response listJobsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Limit != 2 || response.Offset != 1 || len(response.Jobs) != 2 {
		t.Fatalf("unexpected page: %+v", response)
	}
	if response.Jobs[0].ID != rows[2].id || response.Jobs[1].ID != rows[1].id {
		t.Fatalf("unexpected ordering: %+v", response.Jobs)
	}
	if response.Jobs[0].FileKey != nil || response.Jobs[0].ResultKey != nil || response.Jobs[0].ErrorMessage != nil || response.Jobs[0].StartedAt != nil || response.Jobs[0].CompletedAt != nil {
		t.Fatalf("unexpected nullable values: %+v", response.Jobs[0])
	}
}

// TestGetJobHandlerPostgres는 HTTP 단건 조회부터 실제 PostgreSQL 조회까지 검증합니다.
func TestGetJobHandlerPostgres(t *testing.T) {
	pool := integrationPool(t)
	repository := NewRepository(pool)
	handler := NewHandler(NewService(repository))
	ctx := context.Background()

	fileKey := "uploads/access.log"
	created, err := repository.Create(ctx, CreateParams{FileName: "access.log", FileKey: &fileKey})
	if err != nil {
		t.Fatalf("create test job: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, jobsPathPrefix+created.ID, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response jobResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID != created.ID || response.Status != created.Status || response.FileName != created.FileName || response.FileKey == nil || *response.FileKey != fileKey || !response.CreatedAt.Equal(created.CreatedAt) || !response.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.ResultKey != nil || response.ErrorMessage != nil || response.StartedAt != nil || response.CompletedAt != nil {
		t.Fatalf("unexpected nullable values: %+v", response)
	}

	// 유효하지 않은 UUID와 존재하지 않는 UUID의 상태 코드도 실제 Repository로 확인합니다.
	invalidRequest := httptest.NewRequest(http.MethodGet, jobsPathPrefix+"not-a-uuid", nil)
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid id 400, got %d", invalidRecorder.Code)
	}

	missingRequest := httptest.NewRequest(http.MethodGet, jobsPathPrefix+"00000000-0000-0000-0000-000000000999", nil)
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("expected missing job 404, got %d", missingRecorder.Code)
	}
}
