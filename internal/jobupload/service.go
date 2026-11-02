// Package jobupload stores bounded input files before submitting Jobs.
package jobupload

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobsubmission"
)

type Storage interface {
	Put(context.Context, string, io.Reader) error
	Delete(context.Context, string) error
}

type Submitter interface {
	Create(context.Context, job.CreateParams) (job.Job, error)
}

type Service struct {
	storage   Storage
	submitter Submitter
}

func NewService(storage Storage, submitter Submitter) *Service {
	return &Service{storage: storage, submitter: submitter}
}

// Upload keeps the input when a Job may already reference it, including ambiguous
// database failures and publication failures after durable Job creation.
func (s *Service) Upload(ctx context.Context, name string, content io.Reader) (job.Job, error) {
	if s == nil || s.storage == nil || s.submitter == nil || ctx == nil || content == nil {
		return job.Job{}, errors.New("upload job: dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return job.Job{}, err
	}
	if err := job.ValidateUploadName(name); err != nil {
		return job.Job{}, err
	}
	reader := bufio.NewReader(content)
	if _, err := reader.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return job.Job{}, job.ErrInvalidInput
		}
		return job.Job{}, &operationError{operation: "upload job: read input", cause: err}
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return job.Job{}, &operationError{operation: "upload job: generate key", cause: err}
	}
	idBytes[6] = (idBytes[6] & 0x0f) | 0x40
	idBytes[8] = (idBytes[8] & 0x3f) | 0x80
	key := fmt.Sprintf("uploads/%x-%x-%x-%x-%x/access.log", idBytes[:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:])
	bounded := &boundedReader{reader: reader, remaining: job.MaxUploadBytes}
	if err := s.storage.Put(ctx, key, bounded); err != nil {
		return job.Job{}, s.cleanup(ctx, key, &operationError{operation: "upload job: store input", cause: err})
	}
	// No create has started, so cancellation here is safe to clean up.
	if err := ctx.Err(); err != nil {
		return job.Job{}, s.cleanup(ctx, key, err)
	}
	created, err := s.submitter.Create(ctx, job.CreateParams{FileName: name, FileKey: &key})
	if err != nil {
		safeErr := &operationError{operation: "upload job: submit", cause: err}
		if !errors.Is(err, jobsubmission.ErrJobCreated) && (errors.Is(err, job.ErrNotCreated) || errors.Is(err, job.ErrInvalidInput)) {
			return job.Job{}, s.cleanup(ctx, key, safeErr)
		}
		return job.Job{}, safeErr
	}
	return created, nil
}

func (s *Service) cleanup(ctx context.Context, key string, cause error) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.storage.Delete(cleanupContext, key); err != nil {
		return errors.Join(cause, &operationError{operation: "upload job: clean input", cause: err})
	}
	return cause
}

// boundedReader reports overflow to atomic Storage.Put before it can commit.
type boundedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, job.ErrUploadTooLarge
	}
	return n, err
}

type operationError struct {
	operation string
	cause     error
}

func (e *operationError) Error() string { return e.operation }
func (e *operationError) Unwrap() error { return e.cause }
