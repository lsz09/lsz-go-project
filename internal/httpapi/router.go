package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

type healthResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	Timestamp string `json:"timestamp"`
}

// NewRouter는 상태 확인과 Job API Handler를 하나의 HTTP Router로 구성합니다.
func NewRouter(database DatabasePinger, jobHandler http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/ready", readinessHandler(database))

	// Job 의존성이 주입된 경우에만 작업 API 경로를 등록합니다.
	if jobHandler != nil {
		mux.Handle("/api/v1/jobs", jobHandler)
	}

	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := healthResponse{
		Status:    "ok",
		Service:   "cloudqueue-api",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
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
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(append(payload, '\n'))
}
