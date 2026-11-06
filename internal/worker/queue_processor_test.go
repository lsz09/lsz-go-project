package worker

import (
	"context"
	"errors"
	"testing"

	"cloudqueue/internal/job"
)

type queueJobReader struct {
	find func(context.Context, string) (job.Job, error)
}

func (r queueJobReader) FindByID(ctx context.Context, id string) (job.Job, error) {
	return r.find(ctx, id)
}

func TestQueueProcessorStatePolicy(t *testing.T) {
	for _, status := range []job.Status{job.StatusPending, job.StatusCompleted, job.StatusProcessing, job.StatusFailed, "UNKNOWN"} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.WithValue(context.Background(), struct{}{}, "caller")
			calls := 0
			reader := queueJobReader{find: func(received context.Context, id string) (job.Job, error) {
				if received != ctx || id != runnerTestJobID {
					t.Fatal("lookup input changed")
				}
				return job.Job{ID: id, Status: status}, nil
			}}
			processor := &runnerProcessor{process: func(received context.Context, id string) error {
				calls++
				if received != ctx || id != runnerTestJobID {
					t.Fatal("processor input changed")
				}
				return nil
			}}
			err := NewQueueProcessor(reader, processor).ProcessJob(ctx, runnerTestJobID)
			wantCalls := 0
			if status == job.StatusPending {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("processor calls=%d want %d", calls, wantCalls)
			}
			if status == job.StatusPending || status == job.StatusCompleted {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, job.ErrInvalidTransition) {
				t.Fatalf("unexpected state error %v", err)
			}
		})
	}
}

func TestQueueProcessorFailures(t *testing.T) {
	for _, cause := range []error{job.ErrNotFound, errors.New("database secret"), context.Canceled} {
		reader := queueJobReader{find: func(context.Context, string) (job.Job, error) { return job.Job{}, cause }}
		if err := NewQueueProcessor(reader, &runnerProcessor{}).ProcessJob(context.Background(), runnerTestJobID); !errors.Is(err, cause) {
			t.Fatalf("lost lookup cause: %v", err)
		}
	}
	reader := queueJobReader{find: func(context.Context, string) (job.Job, error) {
		return job.Job{ID: runnerTestJobID, Status: job.StatusPending}, nil
	}}
	processor := &runnerProcessor{process: func(context.Context, string) error { return job.ErrInvalidTransition }}
	if err := NewQueueProcessor(reader, processor).ProcessJob(context.Background(), runnerTestJobID); !errors.Is(err, job.ErrInvalidTransition) {
		t.Fatal("acquisition conflict acknowledged")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewQueueProcessor(queueJobReader{}, processor).ProcessJob(ctx, runnerTestJobID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := NewQueueProcessor(nil, processor).ProcessJob(context.Background(), runnerTestJobID); err == nil {
		t.Fatal("missing reader accepted")
	}
}

func TestQueueProcessorAcceptsEquivalentUUIDCase(t *testing.T) {
	const lower = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	reader := queueJobReader{find: func(context.Context, string) (job.Job, error) {
		return job.Job{ID: lower, Status: job.StatusCompleted}, nil
	}}
	if err := NewQueueProcessor(reader, &runnerProcessor{}).ProcessJob(context.Background(), "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"); err != nil {
		t.Fatal(err)
	}
}
