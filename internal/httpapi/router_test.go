package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	t.Run("GET 요청에 정상 상태를 반환한다", func(t *testing.T) {
		router := NewRouter(nil, nil)

		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusOK,
				recorder.Code,
			)
		}

		var response healthResponse

		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if response.Status != "ok" {
			t.Errorf("expected status ok, got %q", response.Status)
		}

		if response.Service != "cloudqueue-api" {
			t.Errorf(
				"expected service cloudqueue-api, got %q",
				response.Service,
			)
		}

		if response.Timestamp == "" {
			t.Error("expected timestamp, got empty value")
		}
	})

	t.Run("GET 이외의 요청에는 405를 반환한다", func(t *testing.T) {
		router := NewRouter(nil, nil)

		request := httptest.NewRequest(http.MethodPost, "/health", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusMethodNotAllowed,
				recorder.Code,
			)
		}

		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf(
				"expected Allow header %q, got %q",
				http.MethodGet,
				allow,
			)
		}
	})
}

// TestJobRoute는 등록된 Job Handler가 작업 API 요청을 전달받는지 검증합니다.
func TestJobRoute(t *testing.T) {
	called := false
	jobHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	})
	router := NewRouter(nil, jobHandler)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if !called {
		t.Fatal("expected job handler to be called")
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, recorder.Code)
	}
}
