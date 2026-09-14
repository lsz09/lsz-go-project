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

// TestJobRoute는 컬렉션과 단건 경로가 등록된 Job Handler로 전달되는지 검증합니다.
func TestJobRoute(t *testing.T) {
	paths := []string{
		"/api/v1/jobs",
		"/api/v1/jobs/00000000-0000-0000-0000-000000000001",
		"/api/v1/jobs/",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			calledPath := ""
			jobHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calledPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			})
			router := NewRouter(nil, jobHandler)

			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if calledPath != path {
				t.Fatalf("expected path %q, got %q", path, calledPath)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}
		})
	}
}

// TestUnknownRoute는 Job 경로와 비슷하지만 등록되지 않은 요청이 404인지 검증합니다.
func TestUnknownRoute(t *testing.T) {
	router := NewRouter(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("job handler must not be called")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/job", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}
