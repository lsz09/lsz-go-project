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
	validEnvironment := func(name string) string {
		return map[string]string{
			"DATABASE_URL":       "postgres://user:secret@localhost:5433/cloudqueue",
			"LOCAL_STORAGE_ROOT": "./data",
		}[name]
	}

	tests := []struct {
		name    string
		args    []string
		getenv  func(string) string
		wantErr string
	}{
		{name: "valid", args: []string{"-job-id", "00000000-0000-0000-0000-000000000001"}, getenv: validEnvironment},
		{name: "missing job id", getenv: validEnvironment, wantErr: "-job-id is required"},
		{name: "empty job id", args: []string{"-job-id", ""}, getenv: validEnvironment, wantErr: "-job-id is required"},
		{name: "blank job id", args: []string{"-job-id", "   "}, getenv: validEnvironment, wantErr: "-job-id is required"},
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
			return "  "
		}, wantErr: "LOCAL_STORAGE_ROOT is required"},
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
			if config.jobID != test.args[1] || config.databaseURL != validEnvironment("DATABASE_URL") || config.localStorageRoot != validEnvironment("LOCAL_STORAGE_ROOT") {
				t.Fatalf("unexpected configuration: %+v", config)
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
