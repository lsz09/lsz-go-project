//go:build integration

package job

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
