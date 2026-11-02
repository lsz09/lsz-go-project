package jobupload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobsubmission"
)

type testStorage struct {
	key             string
	data            []byte
	putError        error
	deleteError     error
	deleted         bool
	putContext      context.Context
	deleteContext   context.Context
	cleanupCanceled bool
}

func (s *testStorage) Put(ctx context.Context, key string, content io.Reader) error {
	s.key, s.putContext = key, ctx
	var err error
	s.data, err = io.ReadAll(content)
	return errors.Join(err, s.putError)
}

func (s *testStorage) Delete(ctx context.Context, key string) error {
	s.deleted, s.deleteContext = true, ctx
	s.cleanupCanceled = ctx.Err() != nil
	if key != s.key {
		return errors.New("wrong cleanup key")
	}
	return s.deleteError
}

type testSubmitter struct {
	create func(context.Context, job.CreateParams) (job.Job, error)
}

func (s testSubmitter) Create(ctx context.Context, p job.CreateParams) (job.Job, error) {
	return s.create(ctx, p)
}

func TestUploadStoresBeforeSubmission(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	storage := &testStorage{}
	called := false
	submitter := testSubmitter{create: func(received context.Context, p job.CreateParams) (job.Job, error) {
		called = true
		if received != ctx || storage.putContext != ctx || string(storage.data) != "input log" {
			t.Fatal("input or Context not forwarded")
		}
		if p.FileName != "example.log" || p.FileKey == nil || *p.FileKey != storage.key {
			t.Fatal("incorrect Job parameters")
		}
		parts := strings.Split(storage.key, "/")
		if len(parts) != 3 || parts[0] != "uploads" || parts[2] != "access.log" || job.ValidateID(parts[1]) != nil {
			t.Fatalf("unsafe key %q", storage.key)
		}
		return job.Job{ID: "created", FileKey: p.FileKey}, nil
	}}
	service := NewService(storage, submitter)
	if _, err := service.Upload(ctx, "example.log", strings.NewReader("input log")); err != nil {
		t.Fatal(err)
	}
	firstKey := storage.key
	if _, err := service.Upload(ctx, "example.log", strings.NewReader("input log")); err != nil {
		t.Fatal(err)
	}
	if !called || storage.deleted || firstKey == storage.key {
		t.Fatal("missing submission, unexpected delete, or key collision")
	}
}

func TestUploadFailureCleanupPolicy(t *testing.T) {
	providerError := errors.New("password and absolute path secret")
	for _, tc := range []struct {
		name            string
		putError, error error
		deleted         bool
	}{
		{name: "storage failure", putError: providerError, deleted: true},
		{name: "definite DB rejection", error: errors.Join(job.ErrNotCreated, providerError), deleted: true},
		{name: "validation before creation", error: job.ErrInvalidInput, deleted: true},
		{name: "ambiguous DB result", error: providerError},
		{name: "publication failure", error: errors.Join(jobsubmission.ErrJobCreated, providerError)},
		{name: "invalid publication after creation", error: errors.Join(jobsubmission.ErrJobCreated, job.ErrInvalidInput)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &testStorage{putError: tc.putError}
			called := false
			submitter := testSubmitter{create: func(context.Context, job.CreateParams) (job.Job, error) { called = true; return job.Job{}, tc.error }}
			_, err := NewService(storage, submitter).Upload(context.Background(), "access.log", strings.NewReader("data"))
			cause := tc.error
			if tc.putError != nil {
				cause = tc.putError
			}
			if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe or lost error: %v", err)
			}
			if storage.deleted != tc.deleted || (tc.putError != nil && called) {
				t.Fatal("incorrect cleanup or submission")
			}
		})
	}
}

func TestUploadCancellationAndCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	storage := &testStorage{deleteError: errors.New("cleanup secret")}
	submitter := testSubmitter{create: func(context.Context, job.CreateParams) (job.Job, error) {
		cancel()
		return job.Job{}, job.ErrNotCreated
	}}
	_, err := NewService(storage, submitter).Upload(ctx, "access.log", strings.NewReader("data"))
	if !errors.Is(err, job.ErrNotCreated) || !errors.Is(err, storage.deleteError) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("cleanup masked original error: %v", err)
	}
	if !storage.deleted || storage.cleanupCanceled {
		t.Fatal("cleanup used canceled Context")
	}
	if _, ok := storage.deleteContext.Deadline(); !ok {
		t.Fatal("cleanup has no timeout")
	}
}

func TestUploadLimitsAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		content        []byte
		want           error
	}{
		{"empty", "access.log", nil, job.ErrInvalidInput},
		{"oversize", "access.log", bytes.Repeat([]byte("x"), int(job.MaxUploadBytes+1)), job.ErrUploadTooLarge},
		{"unsafe", "../access.log", []byte("data"), job.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &testStorage{}
			submitter := testSubmitter{create: func(context.Context, job.CreateParams) (job.Job, error) {
				t.Fatal("invalid upload was submitted")
				return job.Job{}, nil
			}}
			_, err := NewService(storage, submitter).Upload(context.Background(), tc.filename, bytes.NewReader(tc.content))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewService(&testStorage{}, testSubmitter{}).Upload(ctx, "access.log", strings.NewReader("data")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUploadRejectsMissingDependencies(t *testing.T) {
	var missing *Service
	if _, err := missing.Upload(context.Background(), "access.log", strings.NewReader("data")); err == nil {
		t.Fatal("nil service accepted")
	}
	service := NewService(&testStorage{}, testSubmitter{})
	if _, err := service.Upload(nil, "access.log", strings.NewReader("data")); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := service.Upload(context.Background(), "access.log", nil); err == nil {
		t.Fatal("nil reader accepted")
	}
	for _, name := range []string{"", " ", "a\x00.log", "a\n.log", strings.Repeat("x", 256), "../a.log", `C:\a.log`} {
		if _, err := service.Upload(context.Background(), name, strings.NewReader("data")); !errors.Is(err, job.ErrInvalidInput) {
			t.Fatalf("unsafe filename accepted: %q", name)
		}
	}
}
