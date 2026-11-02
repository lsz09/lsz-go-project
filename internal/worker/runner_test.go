package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloudqueue/internal/jobqueue"
)

const runnerTestJobID = "00000000-0000-0000-0000-000000000001"

type runnerConsumer struct {
	receive func(context.Context) (jobqueue.Delivery, error)
	delete  func(context.Context, string) error
}

func (c *runnerConsumer) Receive(ctx context.Context) (jobqueue.Delivery, error) {
	return c.receive(ctx)
}

func (c *runnerConsumer) Delete(ctx context.Context, receiptHandle string) error {
	return c.delete(ctx, receiptHandle)
}

type runnerProcessor struct {
	process func(context.Context, string) error
}

func (p *runnerProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return p.process(ctx, jobID)
}

func TestQueueRunnerDeletesOnlySuccessfulMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receivedContext := context.Context(nil)
	processedContext := context.Context(nil)
	deletedContext := context.Context(nil)
	consumer := &runnerConsumer{
		receive: func(received context.Context) (jobqueue.Delivery, error) {
			receivedContext = received
			return jobqueue.Delivery{
				Message:       jobqueue.Message{JobID: runnerTestJobID},
				ReceiptHandle: "receipt-secret",
			}, nil
		},
		delete: func(received context.Context, handle string) error {
			deletedContext = received
			if handle != "receipt-secret" {
				t.Fatalf("unexpected receipt handle %q", handle)
			}
			cancel()
			return nil
		},
	}
	processor := &runnerProcessor{process: func(received context.Context, jobID string) error {
		processedContext = received
		if jobID != runnerTestJobID {
			t.Fatalf("unexpected Job ID %q", jobID)
		}
		return nil
	}}

	if err := NewQueueRunner(consumer, processor, nil).Run(ctx); err != nil {
		t.Fatalf("run queue worker: %v", err)
	}
	if receivedContext != ctx || processedContext != ctx || deletedContext != ctx {
		t.Fatal("runner did not preserve the caller Context")
	}
}

func TestQueueRunnerContinuesAfterNoMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiveCalls := 0
	consumer := &runnerConsumer{
		receive: func(context.Context) (jobqueue.Delivery, error) {
			receiveCalls++
			if receiveCalls == 1 {
				return jobqueue.Delivery{}, fmtNoMessage()
			}
			return jobqueue.Delivery{Message: jobqueue.Message{JobID: runnerTestJobID}, ReceiptHandle: "receipt"}, nil
		},
		delete: func(context.Context, string) error {
			cancel()
			return nil
		},
	}
	processor := &runnerProcessor{process: func(context.Context, string) error { return nil }}

	if err := NewQueueRunner(consumer, processor, nil).Run(ctx); err != nil {
		t.Fatalf("run queue worker: %v", err)
	}
	if receiveCalls != 2 {
		t.Fatalf("receive calls = %d, want 2", receiveCalls)
	}
}

func TestQueueRunnerContinuesAfterProcessingFailureWithoutDeleting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiveCalls := 0
	processCalls := 0
	deleted := []string{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	consumer := &runnerConsumer{
		receive: func(context.Context) (jobqueue.Delivery, error) {
			receiveCalls++
			return jobqueue.Delivery{
				Message:       jobqueue.Message{JobID: runnerTestJobID},
				ReceiptHandle: "receipt-secret-" + string(rune('0'+receiveCalls)),
			}, nil
		},
		delete: func(_ context.Context, handle string) error {
			deleted = append(deleted, handle)
			cancel()
			return nil
		},
	}
	processor := &runnerProcessor{process: func(context.Context, string) error {
		processCalls++
		if processCalls == 1 {
			return errors.New("processor secret must not be logged")
		}
		return nil
	}}

	if err := NewQueueRunner(consumer, processor, logger).Run(ctx); err != nil {
		t.Fatalf("run queue worker: %v", err)
	}
	if receiveCalls != 2 || processCalls != 2 {
		t.Fatalf("receive calls=%d process calls=%d, want 2 and 2", receiveCalls, processCalls)
	}
	if len(deleted) != 1 || deleted[0] != "receipt-secret-2" {
		t.Fatalf("deleted handles = %v, want only successful delivery", deleted)
	}
	if strings.Contains(logs.String(), "processor secret") || strings.Contains(logs.String(), "receipt-secret") {
		t.Fatalf("logs exposed sensitive details: %s", logs.String())
	}
	if !strings.Contains(logs.String(), runnerTestJobID) {
		t.Fatal("processing failure log omitted Job ID")
	}
}

func TestQueueRunnerPreservesInfrastructureErrorsWithoutExposingDetails(t *testing.T) {
	tests := []struct {
		name  string
		setup func(error) (*QueueRunner, context.Context)
	}{
		{
			name: "receive",
			setup: func(cause error) (*QueueRunner, context.Context) {
				consumer := &runnerConsumer{receive: func(context.Context) (jobqueue.Delivery, error) {
					return jobqueue.Delivery{}, cause
				}}
				return NewQueueRunner(consumer, &runnerProcessor{}, nil), context.Background()
			},
		},
		{
			name: "delete",
			setup: func(cause error) (*QueueRunner, context.Context) {
				consumer := &runnerConsumer{
					receive: func(context.Context) (jobqueue.Delivery, error) {
						return jobqueue.Delivery{Message: jobqueue.Message{JobID: runnerTestJobID}, ReceiptHandle: "receipt-secret"}, nil
					},
					delete: func(context.Context, string) error { return cause },
				}
				processor := &runnerProcessor{process: func(context.Context, string) error { return nil }}
				return NewQueueRunner(consumer, processor, nil), context.Background()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("provider credential and receipt secret")
			runner, ctx := test.setup(cause)
			err := runner.Run(ctx)
			if !errors.Is(err, cause) {
				t.Fatalf("expected preserved cause, got %v", err)
			}
			if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "receipt") {
				t.Fatalf("error exposed provider details: %v", err)
			}
		})
	}
}

func TestQueueRunnerCancellationIsGraceful(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		consumer := &runnerConsumer{receive: func(context.Context) (jobqueue.Delivery, error) {
			called = true
			return jobqueue.Delivery{}, nil
		}}
		if err := NewQueueRunner(consumer, &runnerProcessor{}, nil).Run(ctx); err != nil {
			t.Fatalf("run queue worker: %v", err)
		}
		if called {
			t.Fatal("consumer was called after cancellation")
		}
	})

	t.Run("during receive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		consumer := &runnerConsumer{receive: func(received context.Context) (jobqueue.Delivery, error) {
			cancel()
			return jobqueue.Delivery{}, received.Err()
		}}
		if err := NewQueueRunner(consumer, &runnerProcessor{}, nil).Run(ctx); err != nil {
			t.Fatalf("run queue worker: %v", err)
		}
	})

	t.Run("during processing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		deleted := false
		consumer := &runnerConsumer{
			receive: func(context.Context) (jobqueue.Delivery, error) {
				return jobqueue.Delivery{Message: jobqueue.Message{JobID: runnerTestJobID}, ReceiptHandle: "receipt"}, nil
			},
			delete: func(context.Context, string) error {
				deleted = true
				return nil
			},
		}
		processor := &runnerProcessor{process: func(received context.Context, _ string) error {
			cancel()
			return received.Err()
		}}
		if err := NewQueueRunner(consumer, processor, nil).Run(ctx); err != nil {
			t.Fatalf("run queue worker: %v", err)
		}
		if deleted {
			t.Fatal("canceled delivery was deleted")
		}
	})
}

func TestQueueRunnerRejectsMissingDependencies(t *testing.T) {
	validConsumer := &runnerConsumer{}
	validProcessor := &runnerProcessor{}
	tests := []struct {
		name   string
		runner *QueueRunner
		ctx    context.Context
	}{
		{name: "nil runner", runner: nil, ctx: context.Background()},
		{name: "nil consumer", runner: NewQueueRunner(nil, validProcessor, nil), ctx: context.Background()},
		{name: "nil processor", runner: NewQueueRunner(validConsumer, nil, nil), ctx: context.Background()},
		{name: "nil context", runner: NewQueueRunner(validConsumer, validProcessor, nil), ctx: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.runner.Run(test.ctx); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func fmtNoMessage() error {
	return errors.Join(errors.New("poll completed"), jobqueue.ErrNoMessage)
}

func TestQueueRunnerDoesNotSpinAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	consumer := &runnerConsumer{receive: func(context.Context) (jobqueue.Delivery, error) {
		calls++
		cancel()
		return jobqueue.Delivery{}, fmtNoMessage()
	}}
	if err := NewQueueRunner(consumer, &runnerProcessor{}, nil).Run(ctx); err != nil {
		t.Fatalf("run queue worker: %v", err)
	}
	if calls != 1 {
		t.Fatalf("receive calls = %d, want 1", calls)
	}
}

func TestQueueRunnerReceiveHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	consumer := &runnerConsumer{receive: func(received context.Context) (jobqueue.Delivery, error) {
		<-received.Done()
		return jobqueue.Delivery{}, received.Err()
	}}
	if err := NewQueueRunner(consumer, &runnerProcessor{}, nil).Run(ctx); err != nil {
		t.Fatalf("deadline shutdown should be graceful, got %v", err)
	}
}
