//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	"cloudqueue/internal/jobsubmission"
	"cloudqueue/internal/jobupload"
	"cloudqueue/internal/loganalysis"
	"cloudqueue/internal/s3storage"
	"cloudqueue/internal/storage"
	workerpkg "cloudqueue/internal/worker"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func workerS3Store(t *testing.T) (storage.Config, storage.Store) {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT_URL")
	if endpoint == "" {
		t.Fatal("integration tests require TEST_S3_ENDPOINT_URL pointing to LocalStack")
	}
	region := os.Getenv("TEST_AWS_REGION")
	if region == "" {
		region = "ap-northeast-2"
	}
	config := storage.Config{Backend: "s3", S3: s3storage.Config{EndpointURL: endpoint, Bucket: fmt.Sprintf("cloudqueue-s3-test-%d", time.Now().UnixNano()), Region: region, AccessKeyID: "test", SecretAccessKey: "test", UsePathStyle: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := s3storage.NewClient(ctx, config.S3)
	if err != nil {
		t.Fatal(err)
	}
	input := &s3.CreateBucketInput{Bucket: aws.String(config.S3.Bucket)}
	if region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(region)}
	}
	if _, err := client.CreateBucket(ctx, input); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(config.S3.Bucket)})
		for pages.HasMorePages() {
			page, err := pages.NextPage(cleanup)
			if err != nil {
				t.Errorf("list test objects: %v", err)
				return
			}
			for _, object := range page.Contents {
				if _, err := client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(config.S3.Bucket), Key: object.Key}); err != nil {
					t.Errorf("delete test object: %v", err)
				}
			}
		}
		if _, err := client.DeleteBucket(cleanup, &s3.DeleteBucketInput{Bucket: aws.String(config.S3.Bucket)}); err != nil {
			t.Errorf("delete test bucket: %v", err)
		}
	})
	store, err := storage.New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return config, store
}

func readS3Object(t *testing.T, ctx context.Context, store storage.Store, key string) []byte {
	t.Helper()
	reader, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return data
}

type failedResultStore struct{ storage.Store }

func (s failedResultStore) Put(ctx context.Context, key string, content io.Reader) error {
	if strings.HasPrefix(key, "results/") {
		return errors.New("result storage unavailable")
	}
	return s.Store.Put(ctx, key, content)
}

const s3ValidLog = "127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n" +
	"127.0.0.1 - - [16/Sep/2026:10:01:00 +0900] \"POST /api/v1/jobs HTTP/1.1\" 500 128 \"-\" \"Mozilla/5.0\"\n"

func TestS3UploadedJobIntegration(t *testing.T) {
	for _, scenario := range []string{"success", "invalid log", "publish failure", "missing input", "result failure"} {
		t.Run(scenario, func(t *testing.T) {
			pool := workerIntegrationPool(t)
			config, apiStore := workerS3Store(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Independent clients demonstrate that API and Worker share objects, not a local directory.
			workerStore, err := storage.New(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			client, queueURL := workerLocalStackQueue(t)
			publisher, consumer := workerQueueAdapters(t, client, queueURL)
			var queuePublisher jobqueue.Publisher = publisher
			if scenario == "publish failure" {
				queuePublisher = failedUploadPublisher{}
			}
			repository := job.NewRepository(pool)
			submission := jobsubmission.NewService(job.NewService(repository), queuePublisher)
			handler := job.NewHandlerWithUploader(submission, jobupload.NewService(apiStore, submission))
			content := s3ValidLog
			if scenario == "invalid log" {
				content = "invalid log\n"
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "access.log")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, content); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("POST", "/api/v1/jobs", &body).WithContext(ctx)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantCode := 201
			if scenario == "publish failure" {
				wantCode = 500
			}
			if response.Code != wantCode {
				t.Fatalf("upload status %d: %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatal("provider error exposed")
			}
			jobs, err := repository.List(ctx, job.ListOptions{})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("Job not persisted: %v", err)
			}
			created := jobs[0]
			if created.FileKey == nil || created.Status != job.StatusPending {
				t.Fatal("invalid PENDING Job")
			}
			if got := readS3Object(t, ctx, workerStore, *created.FileKey); string(got) != content {
				t.Fatal("S3 input content differs")
			}
			if scenario == "publish failure" {
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("unexpected published message: %v", err)
				}
				return
			}
			if scenario == "missing input" {
				if err := apiStore.Delete(ctx, *created.FileKey); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "result failure" {
				workerStore = failedResultStore{workerStore}
			}
			processor := workerpkg.NewQueueProcessor(job.NewService(repository), newJobProcessorWithStorage(pool, workerStore))
			runContext, stop := context.WithCancel(ctx)
			defer stop()
			var runConsumer jobqueue.Consumer = consumer
			var runProcessor workerpkg.JobProcessor = processor
			if scenario == "success" {
				runConsumer = &cancelAfterDeleteConsumer{Consumer: consumer, cancel: stop}
			} else {
				runProcessor = &cancelAfterProcess{processor: processor, cancel: stop}
			}
			runner, err := workerpkg.NewQueueRunnerWithVisibility(runConsumer, runProcessor, consumer, nil, workerpkg.VisibilityConfig{TimeoutSeconds: 2, HeartbeatInterval: 250 * time.Millisecond, RequestTimeout: 500 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(runContext); err != nil {
				t.Fatal(err)
			}
			stored, err := repository.FindByID(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				if stored.Status != job.StatusCompleted || stored.ResultKey == nil || *stored.ResultKey != "results/"+created.ID+".json" {
					t.Fatalf("unexpected completed Job %+v", stored)
				}
				var result loganalysis.Result
				if err := json.Unmarshal(readS3Object(t, ctx, apiStore, *stored.ResultKey), &result); err != nil {
					t.Fatal(err)
				}
				if result.TotalRequests != 2 || result.ErrorRequests != 1 || result.ErrorRate != 0.5 {
					t.Fatalf("incorrect analysis %+v", result)
				}
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("success message remains: %v", err)
				}
			} else {
				if stored.Status != job.StatusFailed || stored.ResultKey != nil {
					t.Fatalf("failed input falsely completed: %+v", stored)
				}
				delivery := workerReceiveVisibleMessage(t, ctx, consumer)
				if delivery.Message.JobID != created.ID {
					t.Fatal("incorrect retained delivery")
				}
				if body, err := apiStore.Open(ctx, "results/"+created.ID+".json"); err == nil {
					_ = body.Close()
					t.Fatal("failed Job has result object")
				}
			}
		})
	}
}

func TestS3OneShotCommandIntegration(t *testing.T) {
	pool := workerIntegrationPool(t)
	storeConfig, store := workerS3Store(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key := "uploads/one-shot/access.log"
	if err := store.Put(ctx, key, strings.NewReader(s3ValidLog)); err != nil {
		t.Fatal(err)
	}
	repository := job.NewRepository(pool)
	created, err := repository.Create(ctx, job.CreateParams{FileName: "access.log", FileKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	// Execute the real command assembly against this test's isolated schema.
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	dsn.RawQuery = query.Encode()
	config := configuration{jobID: created.ID, databaseURL: dsn.String(), storage: storeConfig}
	if err := execute(ctx, config); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.FindByID(ctx, created.ID)
	if err != nil || stored.Status != job.StatusCompleted || stored.ResultKey == nil {
		t.Fatalf("one-shot S3 failed: %+v %v", stored, err)
	}
	if !json.Valid(readS3Object(t, ctx, store, *stored.ResultKey)) {
		t.Fatal("one-shot result invalid")
	}
	if err := store.Delete(ctx, "uploads/missing.log"); err != nil {
		t.Fatalf("missing object delete: %v", err)
	}
}
