package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"cloudqueue/internal/job"
)

// stubJobService replaces job state persistence in Worker unit tests.
type stubJobService struct {
	markProcessing func(context.Context, string) (job.Job, error)
	markCompleted  func(context.Context, string, string) (job.Job, error)
	markFailed     func(context.Context, string, string) (job.Job, error)
}

// MarkProcessing runs the behavior configured by the current test.
func (s stubJobService) MarkProcessing(ctx context.Context, id string) (job.Job, error) {
	return s.markProcessing(ctx, id)
}

// MarkCompleted runs the behavior configured by the current test.
func (s stubJobService) MarkCompleted(ctx context.Context, id string, resultKey string) (job.Job, error) {
	return s.markCompleted(ctx, id, resultKey)
}

// MarkFailed runs the behavior configured by the current test.
func (s stubJobService) MarkFailed(ctx context.Context, id string, errorMessage string) (job.Job, error) {
	return s.markFailed(ctx, id, errorMessage)
}

// stubProcessor replaces the actual file processing in Worker unit tests.
type stubProcessor struct {
	process func(context.Context, job.Job) (string, error)
}

// Process runs the behavior configured by the current test.
func (s stubProcessor) Process(ctx context.Context, current job.Job) (string, error) {
	return s.process(ctx, current)
}

// TestProcessJobCompletesInOrder verifies the successful orchestration sequence and inputs.
func TestProcessJobCompletesInOrder(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	const resultKey = "results/access.json"
	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-1")
	current := job.Job{ID: jobID, Status: job.StatusProcessing, FileName: "access.log"}
	var calls []string

	service := stubJobService{
		markProcessing: func(gotContext context.Context, gotID string) (job.Job, error) {
			calls = append(calls, "processing")
			assertContextAndID(t, gotContext, requestIDKey, gotID, jobID)
			return current, nil
		},
		markCompleted: func(gotContext context.Context, gotID string, gotResultKey string) (job.Job, error) {
			calls = append(calls, "completed")
			assertContextAndID(t, gotContext, requestIDKey, gotID, jobID)
			if gotResultKey != resultKey {
				t.Fatalf("expected result key %q, got %q", resultKey, gotResultKey)
			}
			return job.Job{ID: jobID, Status: job.StatusCompleted}, nil
		},
		markFailed: func(context.Context, string, string) (job.Job, error) {
			t.Fatal("MarkFailed must not be called")
			return job.Job{}, nil
		},
	}
	processor := stubProcessor{process: func(gotContext context.Context, gotJob job.Job) (string, error) {
		calls = append(calls, "processor")
		if gotContext.Value(requestIDKey) != "request-1" {
			t.Fatal("caller context was not propagated to Processor")
		}
		if !reflect.DeepEqual(gotJob, current) {
			t.Fatalf("expected job %+v, got %+v", current, gotJob)
		}
		return resultKey, nil
	}}

	if err := New(service, processor).ProcessJob(ctx, jobID); err != nil {
		t.Fatalf("process job: %v", err)
	}
	if want := []string{"processing", "processor", "completed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("expected calls %v, got %v", want, calls)
	}
}

// TestProcessJobStopsWhenMarkProcessingFails verifies that unclaimed jobs are never processed.
func TestProcessJobStopsWhenMarkProcessingFails(t *testing.T) {
	causes := []error{
		job.ErrInvalidInput,
		job.ErrNotFound,
		job.ErrInvalidTransition,
		errors.New("database unavailable"),
		context.Canceled,
		context.DeadlineExceeded,
	}

	for _, cause := range causes {
		t.Run(cause.Error(), func(t *testing.T) {
			service := stubJobService{
				markProcessing: func(context.Context, string) (job.Job, error) { return job.Job{}, cause },
				markCompleted: func(context.Context, string, string) (job.Job, error) {
					t.Fatal("MarkCompleted must not be called")
					return job.Job{}, nil
				},
				markFailed: func(context.Context, string, string) (job.Job, error) {
					t.Fatal("MarkFailed must not be called")
					return job.Job{}, nil
				},
			}
			processor := stubProcessor{process: func(context.Context, job.Job) (string, error) {
				t.Fatal("Processor must not be called")
				return "", nil
			}}

			err := New(service, processor).ProcessJob(context.Background(), "job-id")
			if !errors.Is(err, cause) || err == cause {
				t.Fatalf("expected wrapped cause %v, got %v", cause, err)
			}
		})
	}
}

// TestProcessJobMarksProcessorFailure verifies failure persistence and error preservation.
func TestProcessJobMarksProcessorFailure(t *testing.T) {
	const jobID = "job-id"
	processErr := errors.New("unsupported file format")
	current := job.Job{ID: jobID, Status: job.StatusProcessing}
	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-2")

	service := stubJobService{
		markProcessing: func(gotContext context.Context, _ string) (job.Job, error) {
			if gotContext != ctx {
				t.Fatal("caller context was not propagated to MarkProcessing")
			}
			return current, nil
		},
		markCompleted: func(context.Context, string, string) (job.Job, error) {
			t.Fatal("MarkCompleted must not be called")
			return job.Job{}, nil
		},
		markFailed: func(gotContext context.Context, gotID string, gotMessage string) (job.Job, error) {
			if gotContext != ctx {
				t.Fatal("caller context was not propagated to MarkFailed")
			}
			if gotID != jobID || gotMessage != processErr.Error() {
				t.Fatalf("unexpected failure input: %q %q", gotID, gotMessage)
			}
			return job.Job{ID: jobID, Status: job.StatusFailed}, nil
		},
	}
	processor := stubProcessor{process: func(gotContext context.Context, gotJob job.Job) (string, error) {
		if gotContext != ctx || !reflect.DeepEqual(gotJob, current) {
			t.Fatal("caller context or current job was not propagated to Processor")
		}
		return "", processErr
	}}

	err := New(service, processor).ProcessJob(ctx, jobID)
	if !errors.Is(err, processErr) || err == processErr {
		t.Fatalf("expected wrapped processor error, got %v", err)
	}
}

// TestProcessJobPreservesProcessorAndMarkFailedErrors verifies errors.Join behavior.
func TestProcessJobPreservesProcessorAndMarkFailedErrors(t *testing.T) {
	processErr := errors.New("processing failed")
	markFailedErr := errors.New("database write failed")
	service := stubJobService{
		markProcessing: func(context.Context, string) (job.Job, error) {
			return job.Job{ID: "job-id", Status: job.StatusProcessing}, nil
		},
		markFailed: func(context.Context, string, string) (job.Job, error) {
			return job.Job{}, markFailedErr
		},
	}
	processor := stubProcessor{process: func(context.Context, job.Job) (string, error) {
		return "", processErr
	}}

	err := New(service, processor).ProcessJob(context.Background(), "job-id")
	if !errors.Is(err, processErr) || !errors.Is(err, markFailedErr) {
		t.Fatalf("expected both errors to be preserved, got %v", err)
	}
}

// TestProcessJobPreservesMarkCompletedError verifies completion persistence errors remain inspectable.
func TestProcessJobPreservesMarkCompletedError(t *testing.T) {
	completionErr := errors.New("database write failed")
	service := stubJobService{
		markProcessing: func(context.Context, string) (job.Job, error) {
			return job.Job{ID: "job-id", Status: job.StatusProcessing}, nil
		},
		markCompleted: func(context.Context, string, string) (job.Job, error) {
			return job.Job{}, completionErr
		},
	}
	processor := stubProcessor{process: func(context.Context, job.Job) (string, error) {
		return "result.json", nil
	}}

	err := New(service, processor).ProcessJob(context.Background(), "job-id")
	if !errors.Is(err, completionErr) || err == completionErr {
		t.Fatalf("expected wrapped completion error, got %v", err)
	}
}

// TestProcessJobStopsBeforeProcessorAfterCancellation verifies cancellation between orchestration steps.
func TestProcessJobStopsBeforeProcessorAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := stubJobService{
		markProcessing: func(context.Context, string) (job.Job, error) {
			cancel()
			return job.Job{ID: "job-id", Status: job.StatusProcessing}, nil
		},
		markCompleted: func(context.Context, string, string) (job.Job, error) {
			t.Fatal("MarkCompleted must not be called after cancellation")
			return job.Job{}, nil
		},
		markFailed: func(context.Context, string, string) (job.Job, error) {
			t.Fatal("MarkFailed must not be called after cancellation")
			return job.Job{}, nil
		},
	}
	processor := stubProcessor{process: func(context.Context, job.Job) (string, error) {
		t.Fatal("Processor must not be called after cancellation")
		return "", nil
	}}

	err := New(service, processor).ProcessJob(ctx, "job-id")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

// TestProcessJobRejectsMissingConfiguration verifies invalid workers return errors instead of panicking.
func TestProcessJobRejectsMissingConfiguration(t *testing.T) {
	service := stubJobService{markProcessing: func(context.Context, string) (job.Job, error) {
		return job.Job{}, nil
	}}
	processor := stubProcessor{process: func(context.Context, job.Job) (string, error) { return "", nil }}
	var nilWorker *Worker

	tests := []struct {
		name   string
		worker *Worker
		ctx    context.Context
	}{
		{name: "nil worker", worker: nilWorker, ctx: context.Background()},
		{name: "nil service", worker: New(nil, processor), ctx: context.Background()},
		{name: "nil processor", worker: New(service, nil), ctx: context.Background()},
		{name: "nil context", worker: New(service, processor), ctx: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.worker.ProcessJob(test.ctx, "job-id"); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

// assertContextAndID verifies that orchestration forwards the original context and job ID.
func assertContextAndID(t *testing.T, ctx context.Context, key any, gotID string, wantID string) {
	t.Helper()
	if ctx.Value(key) != "request-1" {
		t.Fatal("caller context was not propagated")
	}
	if gotID != wantID {
		t.Fatalf("expected id %q, got %q", wantID, gotID)
	}
}
