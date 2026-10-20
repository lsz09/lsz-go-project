// Package jobsubmission coordinates durable Job creation with queue publication.
package jobsubmission

import (
	"context"
	"errors"
	"fmt"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
)

// JobService is the Job application surface used by the HTTP Handler.
type JobService interface {
	Create(ctx context.Context, params job.CreateParams) (job.Job, error)
	FindByID(ctx context.Context, id string) (job.Job, error)
	List(ctx context.Context, options job.ListOptions) ([]job.Job, error)
}

// Service adds queue publication to Job creation and delegates read operations.
type Service struct {
	jobs      JobService
	publisher jobqueue.Publisher
}

// NewService creates a Job submission service from the existing Job service and queue publisher.
func NewService(jobs JobService, publisher jobqueue.Publisher) *Service {
	return &Service{jobs: jobs, publisher: publisher}
}

// Create stores a PENDING Job first and then publishes its validated ID.
func (s *Service) Create(ctx context.Context, params job.CreateParams) (job.Job, error) {
	if s == nil || s.jobs == nil {
		return job.Job{}, errors.New("submit job: job service is required")
	}
	if s.publisher == nil {
		return job.Job{}, errors.New("submit job: publisher is required")
	}

	created, err := s.jobs.Create(ctx, params)
	if err != nil {
		return job.Job{}, fmt.Errorf("submit job: create: %w", err)
	}

	message, err := jobqueue.NewMessage(created.ID)
	if err != nil {
		return job.Job{}, fmt.Errorf("submit job: create message: %w", err)
	}
	if err := s.publisher.Publish(ctx, message); err != nil {
		// The PENDING Job intentionally remains durable. A later outbox/recovery task
		// will close the consistency gap between PostgreSQL and SQS.
		return job.Job{}, fmt.Errorf("submit job: publish: %w", err)
	}

	return created, nil
}

// FindByID delegates reads so the wrapper can satisfy the existing HTTP contract.
func (s *Service) FindByID(ctx context.Context, id string) (job.Job, error) {
	if s == nil || s.jobs == nil {
		return job.Job{}, errors.New("find submitted job: job service is required")
	}
	return s.jobs.FindByID(ctx, id)
}

// List delegates reads so only the create path gains queue side effects.
func (s *Service) List(ctx context.Context, options job.ListOptions) ([]job.Job, error) {
	if s == nil || s.jobs == nil {
		return nil, errors.New("list submitted jobs: job service is required")
	}
	return s.jobs.List(ctx, options)
}
