//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	"cloudqueue/internal/jobsubmission"
	"cloudqueue/internal/jobupload"
	"cloudqueue/internal/logprocessor"
	workerpkg "cloudqueue/internal/worker"
)

type failedUploadPublisher struct{}

func (failedUploadPublisher) Publish(context.Context, jobqueue.Message) error {
	return errors.New("provider secret")
}

func TestUploadedJobIntegration(t *testing.T) {
	for _, tc := range []struct {
		name, content  string
		status         job.Status
		publishFailure bool
	}{
		{"valid", "127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n", job.StatusCompleted, false},
		{"invalid log", "not a combined log\n", job.StatusFailed, false},
		{"publication failure", "input must remain", job.StatusPending, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := workerIntegrationPool(t)
			client, queueURL := workerLocalStackQueue(t)
			publisher, consumer := workerQueueAdapters(t, client, queueURL)
			var queuePublisher jobqueue.Publisher = publisher
			if tc.publishFailure {
				queuePublisher = failedUploadPublisher{}
			}
			repository := job.NewRepository(pool)
			submission := jobsubmission.NewService(job.NewService(repository), queuePublisher)
			root := t.TempDir()
			handler := job.NewHandlerWithUploader(submission, jobupload.NewService(logprocessor.NewLocalStorage(root), submission))
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "example.log")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = part.Write([]byte(tc.content)); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			request := httptest.NewRequest("POST", "/api/v1/jobs", &body).WithContext(ctx)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := 201
			if tc.publishFailure {
				want = 500
			}
			if response.Code != want {
				t.Fatalf("got %d want %d: %s", response.Code, want, response.Body.String())
			}
			jobs, err := repository.List(ctx, job.ListOptions{})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("Job not persisted: %v", err)
			}
			created := jobs[0]
			if created.FileKey == nil || created.Status != job.StatusPending {
				t.Fatalf("unexpected initial Job %+v", created)
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(*created.FileKey)))
			if err != nil || string(data) != tc.content {
				t.Fatalf("uploaded input missing or changed: %v", err)
			}
			if tc.publishFailure {
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("unexpected queue delivery: %v", err)
				}
				return
			}
			var reply struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.ID != created.ID {
				t.Fatalf("wrong response Job: %v", err)
			}
			runContext, stop := context.WithCancel(ctx)
			defer stop()
			if tc.status == job.StatusCompleted {
				wrapped := &cancelAfterDeleteConsumer{Consumer: consumer, cancel: stop}
				if err := workerpkg.NewQueueRunner(wrapped, newJobProcessor(pool, root), nil).Run(runContext); err != nil {
					t.Fatal(err)
				}
				if !wrapped.deleted {
					t.Fatal("successful message not deleted")
				}
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("message not absent: %v", err)
				}
			} else {
				wrapped := &cancelAfterProcess{processor: newJobProcessor(pool, root), cancel: stop}
				if err := workerpkg.NewQueueRunner(consumer, wrapped, nil).Run(runContext); err != nil {
					t.Fatal(err)
				}
				delivery := workerReceiveVisibleMessage(t, ctx, consumer)
				if delivery.Message.JobID != created.ID {
					t.Fatal("wrong retained message")
				}
			}
			stored, err := repository.FindByID(ctx, created.ID)
			if err != nil || stored.Status != tc.status {
				t.Fatalf("unexpected final Job %+v: %v", stored, err)
			}
			if tc.status == job.StatusCompleted {
				if stored.ResultKey == nil {
					t.Fatal("no result key")
				}
				result, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(*stored.ResultKey)))
				if err != nil || !json.Valid(result) {
					t.Fatalf("invalid analysis result: %v", err)
				}
			}
		})
	}
}
