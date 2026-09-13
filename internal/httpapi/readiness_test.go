package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeDatabasePinger struct {
	err error
}

func (f fakeDatabasePinger) Ping(context.Context) error {
	return f.err
}

func TestReadinessEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		database     DatabasePinger
		wantCode     int
		wantStatus   string
		wantDatabase string
	}{
		{
			name:         "데이터베이스 연결 성공 시 ready를 반환한다",
			database:     fakeDatabasePinger{},
			wantCode:     http.StatusOK,
			wantStatus:   "ready",
			wantDatabase: "connected",
		},
		{
			name: "데이터베이스 연결 실패 시 503을 반환한다",
			database: fakeDatabasePinger{
				err: errors.New("database unavailable"),
			},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantDatabase: "disconnected",
		},
		{
			name:         "데이터베이스가 주입되지 않으면 503을 반환한다",
			database:     nil,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantDatabase: "disconnected",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(test.database, nil)

			request := httptest.NewRequest(
				http.MethodGet,
				"/ready",
				nil,
			)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantCode {
				t.Fatalf(
					"expected status code %d, got %d",
					test.wantCode,
					recorder.Code,
				)
			}

			var response readinessResponse

			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode readiness response: %v", err)
			}

			if response.Status != test.wantStatus {
				t.Errorf(
					"expected status %q, got %q",
					test.wantStatus,
					response.Status,
				)
			}

			if response.Database != test.wantDatabase {
				t.Errorf(
					"expected database status %q, got %q",
					test.wantDatabase,
					response.Database,
				)
			}
		})
	}
}

func TestReadinessEndpointRejectsUnsupportedMethod(t *testing.T) {
	router := NewRouter(fakeDatabasePinger{}, nil)

	request := httptest.NewRequest(
		http.MethodPost,
		"/ready",
		nil,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf(
			"expected status code %d, got %d",
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
}
