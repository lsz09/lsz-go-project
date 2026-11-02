package job

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxUploadBytes int64 = 10 << 20
const MaxUploadRequestBytes int64 = MaxUploadBytes + 1<<20

var ErrUploadTooLarge = errors.New("upload exceeds size limit")

// ValidateUploadName validates the original header value before multipart's
// FileName normalization can remove an unsafe directory prefix.
func ValidateUploadName(name string) error {
	if strings.TrimSpace(name) == "" || !utf8.ValidString(name) || len(name) > 255 ||
		strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") || strings.ContainsFunc(name, unicode.IsControl) {
		return ErrInvalidInput
	}
	return nil
}

// dispatchUpload returns false only for the existing JSON request path.
func (h *Handler) dispatchUpload(w http.ResponseWriter, r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	// Retain legacy JSON clients which did not explicitly set Content-Type.
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
		return true
	}
	if mediaType == "application/json" {
		return false
	}
	if mediaType != "multipart/form-data" {
		writeAPIResponse(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "unsupported media type"})
		return true
	}
	h.uploadJob(w, r)
	return true
}

func (h *Handler) uploadJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadRequestBytes)
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		writeUploadError(w, errors.Join(ErrInvalidInput, err))
		return
	}
	// The multipart parser stops at the closing boundary. Count any remaining
	// epilogue bytes too, so the whole-request limit cannot be bypassed.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeUploadError(w, errors.Join(ErrInvalidInput, err))
		return
	}
	form := r.MultipartForm
	if len(form.Value) != 0 || len(form.File) != 1 || len(form.File["file"]) != 1 {
		writeUploadError(w, ErrInvalidInput)
		return
	}
	header := form.File["file"][0]
	_, disposition, err := mime.ParseMediaType(header.Header.Get("Content-Disposition"))
	if err != nil || ValidateUploadName(disposition["filename"]) != nil || header.Size == 0 {
		writeUploadError(w, ErrInvalidInput)
		return
	}
	if header.Size > MaxUploadBytes {
		writeUploadError(w, ErrUploadTooLarge)
		return
	}
	if h == nil || h.uploader == nil {
		writeUploadError(w, errors.New("upload service is required"))
		return
	}
	file, err := header.Open()
	if err != nil {
		writeUploadError(w, err)
		return
	}
	defer file.Close()
	created, err := h.uploader.Upload(r.Context(), disposition["filename"], file)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	writeAPIResponse(w, http.StatusCreated, createJobResponse{
		ID: created.ID, Status: created.Status, FileName: created.FileName,
		FileKey: created.FileKey, CreatedAt: created.CreatedAt,
	})
}

func writeUploadError(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, ErrUploadTooLarge), errors.As(err, &maxBytes):
		writeAPIResponse(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request too large"})
	case errors.Is(err, ErrInvalidInput), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, http.ErrMissingBoundary), errors.Is(err, http.ErrNotMultipart):
		writeAPIResponse(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid request"})
	default:
		writeAPIResponse(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal server error"})
	}
}
