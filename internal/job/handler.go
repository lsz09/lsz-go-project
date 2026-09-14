package job

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxCreateRequestBytes는 과도한 요청 Body가 메모리를 점유하지 않도록 1MiB로 제한합니다.
	maxCreateRequestBytes int64 = 1 << 20
	// jobsPath는 작업 생성과 목록 조회에 사용하는 컬렉션 경로입니다.
	jobsPath = "/api/v1/jobs"
	// jobsPathPrefix는 작업 ID가 뒤따르는 단건 조회 경로의 접두사입니다.
	jobsPathPrefix = jobsPath + "/"
)

// JobService는 HTTP Handler가 작업 생성과 조회에 사용하는 Service 기능입니다.
type JobService interface {
	Create(ctx context.Context, params CreateParams) (Job, error)
	FindByID(ctx context.Context, id string) (Job, error)
	List(ctx context.Context, options ListOptions) ([]Job, error)
}

// Handler는 Job HTTP 요청을 Service 호출과 JSON 응답으로 변환합니다.
type Handler struct {
	service JobService
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

// jobResponse는 조회 API에서 작업의 전체 상태를 표현합니다.
type jobResponse struct {
	ID           string     `json:"id"`
	Status       Status     `json:"status"`
	FileName     string     `json:"file_name"`
	FileKey      *string    `json:"file_key"`
	ResultKey    *string    `json:"result_key"`
	ErrorMessage *string    `json:"error_message"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
}

// listJobsResponse는 조회된 작업과 적용된 페이지 옵션을 반환합니다.
type listJobsResponse struct {
	Jobs   []jobResponse `json:"jobs"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// apiErrorResponse는 내부 오류 상세를 숨긴 일관된 오류 응답입니다.
type apiErrorResponse struct {
	Error string `json:"error"`
}

// NewHandler는 주입받은 Service로 Job HTTP Handler를 생성합니다.
func NewHandler(service JobService) *Handler {
	return &Handler{service: service}
}

// ServeHTTP는 컬렉션과 단건 경로를 구분해 Job 요청을 처리합니다.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 정확한 컬렉션 경로와 ID가 뒤따르는 단건 경로만 허용합니다.
	switch {
	case r.URL.Path == jobsPath:
		h.serveCollection(w, r)
	case strings.HasPrefix(r.URL.Path, jobsPathPrefix):
		h.serveItem(w, r)
	default:
		writeAPIResponse(w, http.StatusNotFound, apiErrorResponse{Error: "not found"})
	}
}

// serveCollection은 작업 생성과 목록 조회 Method를 분기합니다.
func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createJob(w, r)
	case http.MethodGet:
		h.listJobs(w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeAPIResponse(w, http.StatusMethodNotAllowed, apiErrorResponse{Error: "method not allowed"})
	}
}

// serveItem은 올바른 단건 경로에서 GET Method만 허용합니다.
func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	// 작업 ID가 없거나 추가 경로 구간이 있으면 존재하지 않는 경로로 처리합니다.
	id, ok := jobIDFromPath(r.URL.Path)
	if !ok {
		writeAPIResponse(w, http.StatusNotFound, apiErrorResponse{Error: "not found"})
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIResponse(w, http.StatusMethodNotAllowed, apiErrorResponse{Error: "method not allowed"})
		return
	}

	h.getJob(w, r, id)
}

// createJob은 JSON 요청을 검증하고 새 PENDING 작업을 생성합니다.
func (h *Handler) createJob(w http.ResponseWriter, r *http.Request) {
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

// listJobs는 페이지 쿼리를 검증하고 최신 작업부터 조회합니다.
func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	// 잘못 인코딩된 쿼리를 감지한 뒤 허용된 limit과 offset만 페이지 옵션으로 변환합니다.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}
	options, err := parseListOptions(values)
	if err != nil {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}

	// Service가 주입되지 않은 구성 오류를 내부 오류로 처리합니다.
	if h == nil || h.service == nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	// HTTP 요청 Context와 검증된 페이지 옵션으로 작업 목록을 조회합니다.
	jobs, err := h.service.List(r.Context(), options)
	if errors.Is(err, ErrInvalidInput) {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}
	if err != nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	// nil 조회 결과도 JSON 빈 배열로 일관되게 반환합니다.
	responses := make([]jobResponse, len(jobs))
	for i, current := range jobs {
		responses[i] = newJobResponse(current)
	}

	writeAPIResponse(w, http.StatusOK, listJobsResponse{
		Jobs:   responses,
		Limit:  options.Limit,
		Offset: options.Offset,
	})
}

// getJob은 작업 ID로 단건을 조회하고 Repository 오류를 HTTP 상태로 변환합니다.
func (h *Handler) getJob(w http.ResponseWriter, r *http.Request, id string) {
	// Service가 주입되지 않은 구성 오류를 내부 오류로 처리합니다.
	if h == nil || h.service == nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	// HTTP 요청 Context와 경로에서 추출한 작업 ID로 단건을 조회합니다.
	found, err := h.service.FindByID(r.Context(), id)
	if errors.Is(err, ErrInvalidInput) {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeAPIResponse(w, http.StatusNotFound, apiErrorResponse{Error: "job not found"})
		return
	}
	if err != nil {
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
		return
	}

	writeAPIResponse(w, http.StatusOK, newJobResponse(found))
}

// jobIDFromPath는 단건 경로에서 슬래시가 없는 작업 ID 하나만 추출합니다.
func jobIDFromPath(path string) (string, bool) {
	id := strings.TrimPrefix(path, jobsPathPrefix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// parseListOptions는 목록 조회 쿼리를 제한 범위의 정수 옵션으로 변환합니다.
func parseListOptions(values url.Values) (ListOptions, error) {
	// 알 수 없는 쿼리와 같은 키의 중복 입력을 거부합니다.
	for key, entries := range values {
		if key != "limit" && key != "offset" {
			return ListOptions{}, errors.New("unknown query parameter")
		}
		if len(entries) != 1 {
			return ListOptions{}, errors.New("query parameter must be provided once")
		}
	}

	options := ListOptions{Limit: DefaultListLimit}
	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 0 || limit > MaxListLimit {
			return ListOptions{}, errors.New("invalid limit")
		}
		// Repository 규칙과 동일하게 limit 0은 기본값 20으로 정규화합니다.
		if limit != 0 {
			options.Limit = limit
		}
	}

	if values.Has("offset") {
		offset, err := strconv.Atoi(values.Get("offset"))
		if err != nil || offset < 0 {
			return ListOptions{}, errors.New("invalid offset")
		}
		options.Offset = offset
	}

	return options, nil
}

// newJobResponse는 내부 Job 모델의 모든 필드를 공개 API 형식으로 변환합니다.
func newJobResponse(current Job) jobResponse {
	return jobResponse{
		ID:           current.ID,
		Status:       current.Status,
		FileName:     current.FileName,
		FileKey:      current.FileKey,
		ResultKey:    current.ResultKey,
		ErrorMessage: current.ErrorMessage,
		CreatedAt:    current.CreatedAt,
		UpdatedAt:    current.UpdatedAt,
		StartedAt:    current.StartedAt,
		CompletedAt:  current.CompletedAt,
	}
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
