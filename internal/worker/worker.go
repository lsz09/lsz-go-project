// Package worker coordinates a job's processing and status transitions.
package worker

import (
	"context"
	"errors"
	"fmt"

	"cloudqueue/internal/job"
)

// JobService provides the job status transitions required by a Worker.
type JobService interface {
	MarkProcessing(ctx context.Context, id string) (job.Job, error)
	MarkCompleted(ctx context.Context, id string, resultKey string) (job.Job, error)
	MarkFailed(ctx context.Context, id string, errorMessage string) (job.Job, error)
}

// Processor performs the actual work and returns the stored result key.
type Processor interface {
	Process(ctx context.Context, current job.Job) (string, error)
}

// Worker coordinates job status changes around a Processor invocation.
type Worker struct {
	jobService JobService
	processor  Processor
}

// New creates a Worker with its required collaborators.
func New(jobService JobService, processor Processor) *Worker {
	return &Worker{jobService: jobService, processor: processor}
}

// ProcessJob marks a job as processing and records its completion or failure.
func (w *Worker) ProcessJob(ctx context.Context, jobID string) error {
	if w == nil {
		return errors.New("process job: worker is required")
	}
	if w.jobService == nil {
		return errors.New("process job: job service is required")
	}
	if w.processor == nil {
		return errors.New("process job: processor is required")
	}
	if ctx == nil {
		return errors.New("process job: context is required")
	}

	current, err := w.jobService.MarkProcessing(ctx, jobID)
	if err != nil {
		return fmt.Errorf("process job: mark processing: %w", err)
	}

	// Stop before invoking the Processor when cancellation happens during MarkProcessing.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("process job: execute processor: %w", err)
	}

	resultKey, processErr := w.processor.Process(ctx, current)
	if processErr != nil {
		_, markErr := w.jobService.MarkFailed(ctx, jobID, processErr.Error())
		wrappedProcessErr := fmt.Errorf("process job: execute processor: %w", processErr)
		if markErr != nil {
			return errors.Join(wrappedProcessErr, fmt.Errorf("process job: mark failed: %w", markErr))
		}
		return wrappedProcessErr
	}

	if _, err := w.jobService.MarkCompleted(ctx, jobID, resultKey); err != nil {
		return fmt.Errorf("process job: mark completed: %w", err)
	}

	return nil
}
