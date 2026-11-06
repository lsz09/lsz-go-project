package worker

import (
	"context"
	"errors"
	"strings"

	"cloudqueue/internal/job"
)

// JobReader verifies durable state before a queued delivery is acknowledged.
type JobReader interface {
	FindByID(context.Context, string) (job.Job, error)
}

// QueueProcessor handles duplicate deliveries without changing one-shot semantics.
type QueueProcessor struct {
	jobs      JobReader
	processor JobProcessor
}

func NewQueueProcessor(jobs JobReader, processor JobProcessor) *QueueProcessor {
	return &QueueProcessor{jobs: jobs, processor: processor}
}

func (p *QueueProcessor) ProcessJob(ctx context.Context, id string) error {
	if p == nil || p.jobs == nil || p.processor == nil || ctx == nil {
		return errors.New("process queued job: dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := job.ValidateID(id); err != nil {
		return err
	}
	current, err := p.jobs.FindByID(ctx, id)
	if err != nil {
		return &runnerOperationError{operation: "process queued job: lookup", cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strings.EqualFold(current.ID, id) {
		return errors.New("process queued job: inconsistent identity")
	}
	switch current.Status {
	case job.StatusCompleted:
		return nil
	case job.StatusPending:
		// ProcessJob retains the atomic PENDING acquisition. A conflict here is
		// never interpreted as successful completion or acknowledged.
		return p.processor.ProcessJob(ctx, id)
	default:
		return &runnerOperationError{operation: "process queued job: state is not executable", cause: job.ErrInvalidTransition}
	}
}
