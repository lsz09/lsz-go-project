package jobsubmission

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
)

type stubJobService struct {
	create func(context.Context, job.CreateParams) (job.Job, error)
	find   func(context.Context, string) (job.Job, error)
	list   func(context.Context, job.ListOptions) ([]job.Job, error)
}

func (s stubJobService) Create(ctx context.Context, params job.CreateParams) (job.Job, error) {
	return s.create(ctx, params)
}

func (s stubJobService) FindByID(ctx context.Context, id string) (job.Job, error) {
	return s.find(ctx, id)
}

func (s stubJobService) List(ctx context.Context, options job.ListOptions) ([]job.Job, error) {
	return s.list(ctx, options)
}

type stubPublisher struct {
	publish func(context.Context, jobqueue.Message) error
}

func (s stubPublisher) Publish(ctx context.Context, message jobqueue.Message) error {
	return s.publish(ctx, message)
}

func TestCreateStoresJobThenPublishesMessage(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-1")
	want := job.Job{ID: jobID, Status: job.StatusPending, FileName: "access.log"}
	created := false

	jobs := stubJobService{create: func(gotContext context.Context, params job.CreateParams) (job.Job, error) {
		if gotContext != ctx || params.FileName != want.FileName {
			t.Fatalf("unexpected create input: %+v", params)
		}
		created = true
		return want, nil
	}}
	publisher := stubPublisher{publish: func(gotContext context.Context, message jobqueue.Message) error {
		if !created {
			t.Fatal("message was published before the Job was created")
		}
		if gotContext != ctx || message.JobID != jobID {
			t.Fatalf("unexpected publish input: %+v", message)
		}
		return nil
	}}

	got, err := NewService(jobs, publisher).Create(ctx, job.CreateParams{FileName: want.FileName})
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

func TestCreateDoesNotPublishWhenJobCreationFails(t *testing.T) {
	cause := errors.New("database unavailable")
	jobs := stubJobService{create: func(context.Context, job.CreateParams) (job.Job, error) {
		return job.Job{}, cause
	}}
	publisher := stubPublisher{publish: func(context.Context, jobqueue.Message) error {
		t.Fatal("publisher must not be called")
		return nil
	}}

	_, err := NewService(jobs, publisher).Create(context.Background(), job.CreateParams{FileName: "access.log"})
	if !errors.Is(err, cause) || err == cause {
		t.Fatalf("expected wrapped create error, got %v", err)
	}
}

func TestCreatePreservesPublishFailureAfterJobCreation(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	cause := errors.New("SQS authorization secret must not be exposed")
	created := false
	jobs := stubJobService{create: func(context.Context, job.CreateParams) (job.Job, error) {
		created = true
		return job.Job{ID: jobID, Status: job.StatusPending, FileName: "access.log"}, nil
	}}
	publisher := stubPublisher{publish: func(context.Context, jobqueue.Message) error { return cause }}

	_, err := NewService(jobs, publisher).Create(context.Background(), job.CreateParams{FileName: "access.log"})
	if !created || !errors.Is(err, cause) || err == cause {
		t.Fatalf("expected durable Job and wrapped publish error, got created=%v err=%v", created, err)
	}
}

func TestCreateRejectsInvalidCreatedJobIDBeforePublish(t *testing.T) {
	jobs := stubJobService{create: func(context.Context, job.CreateParams) (job.Job, error) {
		return job.Job{ID: "invalid", Status: job.StatusPending, FileName: "access.log"}, nil
	}}
	publisher := stubPublisher{publish: func(context.Context, jobqueue.Message) error {
		t.Fatal("publisher must not receive an invalid message")
		return nil
	}}

	_, err := NewService(jobs, publisher).Create(context.Background(), job.CreateParams{FileName: "access.log"})
	if !errors.Is(err, jobqueue.ErrInvalidMessage) || !errors.Is(err, job.ErrInvalidInput) {
		t.Fatalf("expected invalid message error, got %v", err)
	}
}

func TestReadOperationsDelegateWithoutPublishing(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	want := job.Job{ID: jobID, Status: job.StatusPending, FileName: "access.log"}
	jobs := stubJobService{
		find: func(_ context.Context, id string) (job.Job, error) {
			if id != jobID {
				t.Fatalf("unexpected ID %q", id)
			}
			return want, nil
		},
		list: func(_ context.Context, options job.ListOptions) ([]job.Job, error) {
			if options.Limit != 10 {
				t.Fatalf("unexpected options: %+v", options)
			}
			return []job.Job{want}, nil
		},
	}
	publisher := stubPublisher{publish: func(context.Context, jobqueue.Message) error {
		t.Fatal("read operations must not publish")
		return nil
	}}
	service := NewService(jobs, publisher)

	found, err := service.FindByID(context.Background(), jobID)
	if err != nil || !reflect.DeepEqual(found, want) {
		t.Fatalf("find: %+v %v", found, err)
	}
	listed, err := service.List(context.Background(), job.ListOptions{Limit: 10})
	if err != nil || !reflect.DeepEqual(listed, []job.Job{want}) {
		t.Fatalf("list: %+v %v", listed, err)
	}
}

func TestServiceRejectsMissingDependencies(t *testing.T) {
	params := job.CreateParams{FileName: "access.log"}
	if _, err := (*Service)(nil).Create(context.Background(), params); err == nil {
		t.Fatal("expected nil service error")
	}
	jobs := stubJobService{create: func(context.Context, job.CreateParams) (job.Job, error) {
		return job.Job{ID: "00000000-0000-0000-0000-000000000001"}, nil
	}}
	if _, err := NewService(jobs, nil).Create(context.Background(), params); err == nil {
		t.Fatal("expected missing publisher error")
	}
}
