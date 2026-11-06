package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"cloudqueue/internal/jobqueue"
)

// JobProcessor is the Job processing surface used by the queue runner.
type JobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
}

// QueueRunner continuously receives queued Jobs and acknowledges only successful work.
type QueueRunner struct {
	consumer  jobqueue.Consumer
	processor JobProcessor
	logger    *slog.Logger
}

// NewQueueRunner creates a long-running queue Worker.
func NewQueueRunner(consumer jobqueue.Consumer, processor JobProcessor, logger *slog.Logger) *QueueRunner {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &QueueRunner{consumer: consumer, processor: processor, logger: logger}
}

// Run polls until the caller cancels the Context or an infrastructure error prevents progress.
func (r *QueueRunner) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("run queue worker: runner is required")
	}
	if r.consumer == nil {
		return errors.New("run queue worker: consumer is required")
	}
	if r.processor == nil {
		return errors.New("run queue worker: job processor is required")
	}
	if ctx == nil {
		return errors.New("run queue worker: context is required")
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		delivery, err := r.consumer.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, jobqueue.ErrNoMessage) {
				continue
			}
			if errors.Is(err, jobqueue.ErrInvalidDelivery) {
				r.logger.Warn("queue message validation failed; message retained")
				continue
			}
			return &runnerOperationError{operation: "run queue worker: receive message", cause: err}
		}

		if delivery.Message.Validate() != nil || strings.TrimSpace(delivery.ReceiptHandle) == "" {
			r.logger.Warn("queue message validation failed; message retained")
			continue
		}

		if err := r.processor.ProcessJob(ctx, delivery.Message.JobID); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.logger.Error("queue job processing failed", "job_id", delivery.Message.JobID)
			continue
		}
		if ctx.Err() != nil {
			return nil
		}

		if err := r.consumer.Delete(ctx, delivery.ReceiptHandle); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return &runnerOperationError{operation: "run queue worker: delete message", cause: err}
		}
	}
}

// runnerOperationError retains the original cause without exposing provider details.
type runnerOperationError struct {
	operation string
	cause     error
}

func (e *runnerOperationError) Error() string {
	return e.operation
}

func (e *runnerOperationError) Unwrap() error {
	return e.cause
}
