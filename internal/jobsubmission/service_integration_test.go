//go:build integration

package jobsubmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	"cloudqueue/internal/jobqueue/sqsqueue"
)

func TestJobCreationHTTPPublishesToLocalStack(t *testing.T) {
	pool := submissionIntegrationPool(t)
	client, queueURL := submissionLocalStackQueue(t)
	publisher, err := sqsqueue.NewPublisher(client, queueURL)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	consumer, err := sqsqueue.NewConsumer(client, queueURL, 1)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	repository := job.NewRepository(pool)
	handler := job.NewHandler(NewService(job.NewService(repository), publisher))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"file_name":"access.log"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		ID     string     `json:"id"`
		Status job.Status `json:"status"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	stored, err := repository.FindByID(context.Background(), response.ID)
	if err != nil {
		t.Fatalf("find stored Job: %v", err)
	}
	if stored.Status != job.StatusPending || response.Status != job.StatusPending {
		t.Fatalf("expected PENDING Job, stored=%s response=%s", stored.Status, response.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	delivery, err := consumer.Receive(ctx)
	if err != nil {
		t.Fatalf("receive message: %v", err)
	}
	if delivery.Message.JobID != stored.ID {
		t.Fatalf("expected message Job ID %q, got %q", stored.ID, delivery.Message.JobID)
	}
	if err := consumer.Delete(ctx, delivery.ReceiptHandle); err != nil {
		t.Fatalf("delete message: %v", err)
	}
}

func TestPublishFailureLeavesPendingJob(t *testing.T) {
	pool := submissionIntegrationPool(t)
	repository := job.NewRepository(pool)
	publishCause := errors.New("queue authorization secret must not be exposed")
	service := NewService(job.NewService(repository), stubPublisher{
		publish: func(context.Context, jobqueue.Message) error { return publishCause },
	})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"file_name":"access.log"}`))
	recorder := httptest.NewRecorder()
	job.NewHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "authorization secret") {
		t.Fatal("publish error details were exposed in the HTTP response")
	}
	jobs, err := repository.List(context.Background(), job.ListOptions{})
	if err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != job.StatusPending {
		t.Fatalf("expected one durable PENDING Job, got %+v", jobs)
	}
}

func submissionIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("integration tests require TEST_DATABASE_URL pointing to a dedicated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := admin.Close(cleanupContext); err != nil {
			t.Errorf("close admin: %v", err)
		}
	})
	schema := fmt.Sprintf("job_submission_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../../migrations/000001_create_jobs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func submissionLocalStackQueue(t *testing.T) (*sqs.Client, string) {
	t.Helper()
	endpointURL := os.Getenv("TEST_SQS_ENDPOINT_URL")
	if endpointURL == "" {
		t.Fatal("integration tests require TEST_SQS_ENDPOINT_URL pointing to LocalStack")
	}
	region := os.Getenv("TEST_AWS_REGION")
	if region == "" {
		region = "ap-northeast-2"
	}
	config := sqsqueue.Config{
		Region:          region,
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		EndpointURL:     endpointURL,
		QueueURL:        endpointURL,
		WaitTimeSeconds: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := sqsqueue.NewClient(ctx, config)
	if err != nil {
		t.Fatalf("new LocalStack client: %v", err)
	}
	queueName := fmt.Sprintf("cloudqueue-submission-test-%d", time.Now().UnixNano())
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	if err != nil {
		t.Fatalf("create test queue: %v", err)
	}
	queueURL := aws.ToString(created.QueueUrl)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := client.DeleteQueue(cleanupContext, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
			t.Errorf("delete test queue: %v", err)
		}
	})
	return client, queueURL
}
