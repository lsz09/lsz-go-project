package job

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// maxCreateRequestBytes는 과도한 요청 Body가 메모리를 점유하지 않도록 1MiB로 제한합니다.
const maxCreateRequestBytes int64 = 1 << 20

// CreateService는 HTTP Handler가 작업 생성에 사용하는 최소 Service 기능입니다.
type CreateService interface {
	Create(ctx context.Context, params CreateParams) (Job, error)
}

// Handler는 Job HTTP 요청을 Service 호출과 JSON 응답으로 변환합니다.
type Handler struct {
	service CreateService
}

// createJobRequest는 작업 생성 API에서 허용하는 JSON 필드입니다.
type createJobRequest struct {
	FileName string  `json:"file_name"`
	FileKey  *string `json:"file_key"`
}

// createJobResponse는 작업 생성 성공 시 클라이언트에 공개할 필드입니다.
type createJobResponse struct {
	ID        string    `json:"id"`
	Status    Status    `json:"status"`
	FileName  string    `json:"file_name"`
	FileKey   *string   `json:"file_key"`
	CreatedAt time.Time `json:"created_at"`
}

// apiErrorResponse는 내부 오류 상세를 숨긴 일관된 오류 응답입니다.
type apiErrorResponse struct {
	Error string `json:"error"`
}

// NewHandler는 주입받은 Service로 Job HTTP Handler를 생성합니다.
func NewHandler(service CreateService) *Handler {
	return &Handler{service: service}
}

// ServeHTTP는 현재 POST 작업 생성 요청만 처리합니다.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 지원하지 않는 Method에는 허용 Method와 405 응답을 반환합니다.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIResponse(w, http.StatusMethodNotAllowed, apiErrorResponse{Error: "method not allowed"})
		return
	}

	// JSON 구조, 알 수 없는 필드, Body 크기와 단일 객체 여부를 검증합니다.
	request, err := decodeCreateJobRequest(w, r)
	if err != nil {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}

	// Service가 주입되지 않은 구성 오류는 공개 상세 없이 500으로 처리합니다.
	if h == nil || h.service == nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	// HTTP 요청 Context를 Service와 Repository까지 그대로 전달합니다.
	created, err := h.service.Create(r.Context(), CreateParams{
		FileName: request.FileName,
		FileKey:  request.FileKey,
	})
	if errors.Is(err, ErrInvalidInput) {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}
	if err != nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	// DB가 생성한 UUID, 기본 상태와 생성 시각을 201 응답으로 반환합니다.
	writeAPIResponse(w, http.StatusCreated, createJobResponse{
		ID:        created.ID,
		Status:    created.Status,
		FileName:  created.FileName,
		FileKey:   created.FileKey,
		CreatedAt: created.CreatedAt,
	})
}

// decodeCreateJobRequest는 제한된 크기의 JSON 객체 하나만 디코딩합니다.
func decodeCreateJobRequest(w http.ResponseWriter, r *http.Request) (createJobRequest, error) {
	// MaxBytesReader로 지정된 크기를 넘는 요청을 디코더 단계에서 중단합니다.
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateRequestBytes)

	// 정의되지 않은 필드를 오타로 간주해 요청을 거부합니다.
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request createJobRequest
	if err := decoder.Decode(&request); err != nil {
		return createJobRequest{}, err
	}

	// 첫 JSON 객체 뒤에는 공백과 EOF만 허용합니다.
	var remaining any
	if err := decoder.Decode(&remaining); !errors.Is(err, io.EOF) {
		if err == nil {
			return createJobRequest{}, errors.New("request body must contain one JSON object")
		}
		return createJobRequest{}, err
	}

	return request, nil
}

// writeAPIResponse는 상태 코드와 JSON Content-Type을 일관되게 기록합니다.
func writeAPIResponse(w http.ResponseWriter, statusCode int, value any) {
	// 먼저 Marshal해 인코딩 실패 전에 성공 상태 코드가 전송되는 것을 방지합니다.
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	// 응답 쓰기 실패 후에는 클라이언트 연결이 끊겼을 수 있어 추가 응답을 시도하지 않습니다.
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return
	}
}
