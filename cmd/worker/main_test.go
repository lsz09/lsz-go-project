package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloudqueue/internal/job"
)

type fakeJobProcessor struct {
	process func(context.Context, string) error
}

func (f *fakeJobProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return f.process(ctx, jobID)
}

func TestLoadConfiguration(t *testing.T) {
	validValues := map[string]string{
		"DATABASE_URL":          "postgres://user:secret@localhost:5433/cloudqueue",
		"LOCAL_STORAGE_ROOT":    "./data",
		"AWS_REGION":            "ap-northeast-2",
		"AWS_ACCESS_KEY_ID":     "test-access-key",
		"AWS_SECRET_ACCESS_KEY": "test-secret-key",
		"SQS_ENDPOINT_URL":      "http://localhost:4566",
		"SQS_QUEUE_URL":         "http://localhost:4566/000000000000/cloudqueue-jobs",
		"SQS_WAIT_TIME_SECONDS": "10",
	}
	validEnvironment := func(name string) string { return validValues[name] }

	tests := []struct {
		name      string
		args      []string
		getenv    func(string) string
		wantErr   string
		wantQueue bool
		wantJobID string
	}{
		{name: "one-shot mode", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: validEnvironment, wantJobID: "00000000-0000-0000-0000-000000000001"},
		{name: "queue mode", getenv: validEnvironment, wantQueue: true},
		{name: "empty job id", args: []string{"-job-id", ""}, getenv: validEnvironment, wantErr: "-job-id must not be empty"},
		{name: "blank job id", args: []string{"-job-id", "   "}, getenv: validEnvironment, wantErr: "-job-id must not be empty"},
		{name: "unknown option", args: []string{"-unknown"}, getenv: validEnvironment, wantErr: "parse CLI options"},
		{name: "positional argument", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001", "extra"}, getenv: validEnvironment, wantErr: "positional arguments are not allowed"},
		{name: "missing database URL", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: func(string) string { return "" }, wantErr: "DATABASE_URL is required"},
		{name: "blank database URL", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: func(name string) string {
			if name == "LOCAL_STORAGE_ROOT" {
				return "./data"
			}
			return "  "
		}, wantErr: "DATABASE_URL is required"},
		{name: "missing storage root", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: func(name string) string {
			if name == "DATABASE_URL" {
				return "postgres://localhost/database"
			}
			return ""
		}, wantErr: "LOCAL_STORAGE_ROOT is required"},
		{name: "blank storage root", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: func(name string) string {
			if name == "DATABASE_URL" {
				return "postgres://localhost/database"
			}
			if name == "LOCAL_STORAGE_ROOT" {
				return "  "
			}
			return ""
		}, wantErr: "LOCAL_STORAGE_ROOT is required"},
		{name: "queue mode missing SQS configuration", getenv: func(name string) string {
			if name == "DATABASE_URL" || name == "LOCAL_STORAGE_ROOT" {
				return validValues[name]
			}
			return ""
		}, wantErr: "queue mode"},
		{name: "one-shot mode does not require SQS configuration", args: []string{"-job-id", "job-id"}, getenv: func(name string) string {
			if name == "DATABASE_URL" || name == "LOCAL_STORAGE_ROOT" {
				return validValues[name]
			}
			return ""
		}, wantJobID: "job-id"},
		{name: "nil environment lookup", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, wantErr: "environment lookup is required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadConfiguration(test.args, test.getenv)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("configuration error exposed environment value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load configuration: %v", err)
			}
			if config.jobID != test.wantJobID || config.databaseURL != validEnvironment("DATABASE_URL") || config.localStorageRoot != validEnvironment("LOCAL_STORAGE_ROOT") {
				t.Fatalf("unexpected configuration: %+v", config)
			}
			if (config.sqs != nil) != test.wantQueue {
				t.Fatalf("queue mode = %t, want %t", config.sqs != nil, test.wantQueue)
			}
			if config.sqs != nil && (config.sqs.QueueURL != validValues["SQS_QUEUE_URL"] || config.sqs.WaitTimeSeconds != 10) {
				t.Fatalf("unexpected queue configuration")
			}
		})
	}
}

func TestRunForwardsContextAndJobID(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller value")
	const jobID = " 00000000-0000-0000-0000-000000000001 "
	called := false
	processor := &fakeJobProcessor{process: func(received context.Context, receivedID string) error {
		called = true
		if received != ctx {
			t.Fatal("run replaced the caller context")
		}
		if receivedID != jobID {
			t.Fatalf("run changed job ID: %q", receivedID)
		}
		return nil
	}}

	if err := run(ctx, processor, jobID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !called {
		t.Fatal("processor was not called")
	}
}

func TestS3OneShotConfigurationDoesNotRequireLocalStorageOrSQS(t *testing.T) {
	values := map[string]string{"DATABASE_URL": "postgres://localhost/test", "STORAGE_BACKEND": "s3", "S3_ENDPOINT_URL": "http://localhost:4566", "S3_BUCKET_NAME": "test-bucket", "S3_USE_PATH_STYLE": "true", "AWS_REGION": "ap-northeast-2", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	config, err := loadConfiguration([]string{"-job-id", "one-shot"}, func(k string) string { return values[k] }, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.storage.Backend != "s3" || config.localStorageRoot != "" || config.sqs != nil {
		t.Fatal("one-shot storage selection incorrect")
	}
}

func TestVisibilityConfigurationOnlyForQueueMode(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL": "postgres://localhost/test", "LOCAL_STORAGE_ROOT": "./data",
		"AWS_REGION": "ap-northeast-2", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"SQS_ENDPOINT_URL": "http://localhost:4566", "SQS_QUEUE_URL": "http://localhost:4566/test", "SQS_WAIT_TIME_SECONDS": "10",
		"SQS_VISIBILITY_TIMEOUT_SECONDS": "",
	}
	getenv := func(k string) string { return values[k] }
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	if _, err := loadConfiguration(nil, getenv, lookup); err == nil {
		t.Fatal("explicitly empty visibility setting accepted")
	}
	if _, err := loadConfiguration([]string{"-job-id", "one-shot"}, getenv, lookup); err != nil {
		t.Fatalf("one-shot unexpectedly uses visibility setting: %v", err)
	}
	delete(values, "SQS_VISIBILITY_TIMEOUT_SECONDS")
	config, err := loadConfiguration(nil, getenv, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.visibility.TimeoutSeconds != 60 || config.visibility.HeartbeatInterval != 20*time.Second {
		t.Fatal("queue defaults not wired")
	}
}

func TestRunPreservesProcessingErrors(t *testing.T) {
	tests := []struct {
		name  string
		cause error
	}{
		{name: "job input", cause: job.ErrInvalidInput},
		{name: "canceled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &fakeJobProcessor{process: func(context.Context, string) error { return test.cause }}
			err := run(context.Background(), processor, "job-id")
			if !errors.Is(err, test.cause) {
				t.Fatalf("expected cause %v, got %v", test.cause, err)
			}
		})
	}
}

func TestSafeOperationErrorPreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("postgres://user:secret@localhost/database")
	err := &safeOperationError{operation: "run worker: connect database", cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("safe operation error did not preserve its cause")
	}
	if strings.Contains(err.Error(), "secret") || err.Error() != "run worker: connect database" {
		t.Fatalf("safe operation error exposed connection details: %v", err)
	}
}

func TestRunRejectsInvalidDependencies(t *testing.T) {
	if err := run(nil, &fakeJobProcessor{}, "job-id"); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("expected nil context error, got %v", err)
	}
	if err := run(context.Background(), nil, "job-id"); err == nil || !strings.Contains(err.Error(), "job processor is required") {
		t.Fatalf("expected nil processor error, got %v", err)
	}
}

func TestRunDoesNotStartWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	processor := &fakeJobProcessor{process: func(context.Context, string) error {
		called = true
		return nil
	}}

	err := run(ctx, processor, "job-id")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
	if called {
		t.Fatal("processor was called with an already canceled context")
	}
}

func TestRunObservesCancellationDuringProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	processor := &fakeJobProcessor{process: func(received context.Context, _ string) error {
		close(started)
		<-received.Done()
		return received.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- run(ctx, processor, "job-id") }()
	<-started
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not propagate cancellation")
	}
}

func TestSignalContextStopCancelsWorkerContext(t *testing.T) {
	ctx, stop := newSignalContext(context.Background())
	processor := &fakeJobProcessor{process: func(received context.Context, _ string) error {
		<-received.Done()
		return received.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- run(ctx, processor, "job-id") }()
	stop()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("signal context cancellation did not reach processor")
	}
}
