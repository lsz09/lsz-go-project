package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

// DatabasePinger는 실제 PostgreSQL과 테스트용 가짜 DB에 공통으로 사용됩니다.
type DatabasePinger interface {
	Ping(ctx context.Context) error
}

type readinessResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

func readinessHandler(database DatabasePinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		response := readinessResponse{
			Status:   "ready",
			Database: "connected",
		}
		statusCode := http.StatusOK

		if database == nil {
			response.Status = "not_ready"
			response.Database = "disconnected"
			statusCode = http.StatusServiceUnavailable
		} else {
			pingContext, cancel := context.WithTimeout(
				r.Context(),
				readinessTimeout,
			)
			defer cancel()

			if err := database.Ping(pingContext); err != nil {
				response.Status = "not_ready"
				response.Database = "disconnected"
				statusCode = http.StatusServiceUnavailable
			}
		}

		payload, err := json.Marshal(response)
		if err != nil {
			http.Error(
				w,
				"failed to encode response",
				http.StatusInternalServerError,
			)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)

		_, _ = w.Write(append(payload, '\n'))
	}
}
