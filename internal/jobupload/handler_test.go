package jobupload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloudqueue/internal/job"
	"cloudqueue/internal/logprocessor"
)

type httpJobs struct{ calls int }

func (s *httpJobs) Create(_ context.Context, p job.CreateParams) (job.Job, error) {
	s.calls++
	return job.Job{ID: "00000000-0000-0000-0000-000000000001", Status: job.StatusPending, FileName: p.FileName, FileKey: p.FileKey}, nil
}
func (*httpJobs) FindByID(context.Context, string) (job.Job, error)        { return job.Job{}, nil }
func (*httpJobs) List(context.Context, job.ListOptions) ([]job.Job, error) { return nil, nil }

func TestMultipartHTTPValidation(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		size           int
		count          int
		extra          bool
		want           int
	}{
		{"valid", "example.log", 10, 1, false, 201},
		{"boundary maximum", "example.log", int(job.MaxUploadBytes), 1, false, 201},
		{"too large", "example.log", int(job.MaxUploadBytes + 1), 1, false, 413},
		{"total request too large", "example.log", int(job.MaxUploadRequestBytes + 1), 1, false, 413},
		{"empty", "example.log", 0, 1, false, 400},
		{"missing", "example.log", 10, 0, false, 400},
		{"multiple", "example.log", 10, 2, false, 400},
		{"extra field", "example.log", 10, 1, true, 400},
		{"path traversal", "../example.log", 10, 1, false, 400},
		{"absolute path", "/example.log", 10, 1, false, 400},
		{"windows path", `C:\secret\example.log`, 10, 1, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			t.Setenv("TMP", temp)
			t.Setenv("TEMP", temp)
			jobs := &httpJobs{}
			handler := job.NewHandlerWithUploader(jobs, NewService(logprocessor.NewLocalStorage(root), jobs))
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for i := 0; i < tc.count; i++ {
				header := textproto.MIMEHeader{}
				header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, tc.filename))
				part, err := writer.CreatePart(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.CopyN(part, repeatReader{}, int64(tc.size)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.extra {
				if err := writer.WriteField("file_key", "../secret"); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", &body)
			request.ContentLength = -1 // enforce actual streamed bytes rather than trusting a size header
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("got %d want %d: %s", response.Code, tc.want, response.Body.String())
			}
			if tc.want == 201 {
				var created struct {
					FileKey  *string    `json:"file_key"`
					FileName string     `json:"file_name"`
					Status   job.Status `json:"status"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.FileKey == nil || created.FileName != tc.filename || created.Status != job.StatusPending {
					t.Fatalf("unexpected response %s", response.Body.String())
				}
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(*created.FileKey)))
				if err != nil || len(data) != tc.size {
					t.Fatalf("incorrect stored content: %v", err)
				}
			} else if jobs.calls != 0 {
				t.Fatal("rejected request created a Job")
			}
			entries, err := os.ReadDir(temp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("multipart temporary files remain: %v", err)
			}
		})
	}
}

type repeatReader struct{}

func (repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestUploadContentTypesAndJSONCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		want                    int
	}{
		{"json", "application/json", `{"file_name":"access.log"}`, 201},
		{"legacy json", "", `{"file_name":"access.log"}`, 201},
		{"unsupported", "text/plain", "data", 415},
		{"missing boundary", "multipart/form-data", "data", 400},
		{"broken body", "multipart/form-data; boundary=abc", "--abc\r\ninvalid", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := &httpJobs{}
			h := job.NewHandlerWithUploader(jobs, NewService(logprocessor.NewLocalStorage(t.TempDir()), jobs))
			r := httptest.NewRequest("POST", "/api/v1/jobs", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestMultipartRejectsOversizedEpilogue(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "access.log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte("input")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/jobs", io.MultiReader(&body, io.LimitReader(repeatReader{}, job.MaxUploadRequestBytes)))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	jobs := &httpJobs{}
	handler := job.NewHandlerWithUploader(jobs, NewService(logprocessor.NewLocalStorage(t.TempDir()), jobs))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 413 || jobs.calls != 0 {
		t.Fatalf("epilogue exceeded request limit: %d, creates=%d", response.Code, jobs.calls)
	}
}
