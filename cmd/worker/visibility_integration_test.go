//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	"cloudqueue/internal/loganalysis"
	"cloudqueue/internal/logprocessor"
	workerpkg "cloudqueue/internal/worker"
)

type heldLogProcessor struct {
	started chan struct{}
	release chan struct{}
	inner   workerpkg.Processor
}

func (p *heldLogProcessor) Process(ctx context.Context, current job.Job) (string, error) {
	close(p.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-p.release:
	}
	return p.inner.Process(ctx, current)
}

type countedVisibility struct {
	inner jobqueue.VisibilityExtender
	calls atomic.Int32
}

func (e *countedVisibility) ExtendVisibility(ctx context.Context, handle string, seconds int32) error {
	if err := e.inner.ExtendVisibility(ctx, handle, seconds); err != nil {
		return err
	}
	e.calls.Add(1)
	return nil
}

func TestVisibilityHeartbeatIntegration(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "long processing stays hidden and deletes", true: "failed processing stops heartbeat and redelivers"}[failure], func(t *testing.T) {
			pool := workerIntegrationPool(t)
			client, queueURL := workerLocalStackQueue(t)
			publisher, consumer := workerQueueAdapters(t, client, queueURL)
			repository := job.NewRepository(pool)
			root := t.TempDir()
			content := "127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n"
			if failure {
				content = "invalid log\n"
			}
			created := workerCreateJobWithLog(t, repository, root, "access.log", content)
			message, err := jobqueue.NewMessage(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := publisher.Publish(ctx, message); err != nil {
				t.Fatal(err)
			}
			held := &heldLogProcessor{started: make(chan struct{}), release: make(chan struct{}), inner: logprocessor.New(logprocessor.NewLocalStorage(root), loganalysis.New())}
			jobService := job.NewService(repository)
			processor := workerpkg.NewQueueProcessor(jobService, workerpkg.New(jobService, held))
			runContext, stop := context.WithCancel(ctx)
			var runConsumer jobqueue.Consumer = &cancelAfterDeleteConsumer{Consumer: consumer, cancel: stop}
			var runProcessor workerpkg.JobProcessor = processor
			if failure {
				runConsumer = consumer
				runProcessor = &cancelAfterProcess{processor: processor, cancel: stop}
			}
			extender := &countedVisibility{inner: consumer}
			config := workerpkg.VisibilityConfig{TimeoutSeconds: 2, HeartbeatInterval: 250 * time.Millisecond, RequestTimeout: 500 * time.Millisecond}
			runner, err := workerpkg.NewQueueRunnerWithVisibility(runConsumer, runProcessor, extender, nil, config)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- runner.Run(runContext) }()
			var waitOnce sync.Once
			wait := func() {
				waitOnce.Do(func() {
					select {
					case err := <-done:
						if err != nil {
							t.Errorf("runner: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Error("runner did not stop")
					}
				})
			}
			defer func() { stop(); wait() }()
			select {
			case <-held.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			stored, err := repository.FindByID(ctx, created.ID)
			if err != nil || stored.Status != job.StatusProcessing {
				t.Fatalf("Job not processing: %v", err)
			}
			// Observe for longer than both the Queue default (1s) and the initial
			// lease (2s), with another Consumer using the real SQS endpoint.
			observationEnd := time.Now().Add(3 * time.Second)
			for time.Now().Before(observationEnd) {
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("inflight message became visible: %v", err)
				}
			}
			if extender.calls.Load() < 3 {
				t.Fatal("periodic extensions were not observed")
			}
			close(held.release)
			wait()
			before := extender.calls.Load()
			stored, err = repository.FindByID(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failure {
				if stored.Status != job.StatusFailed {
					t.Fatalf("expected FAILED, got %s", stored.Status)
				}
				delivery := workerReceiveVisibleMessage(t, ctx, consumer)
				if delivery.Message.JobID != created.ID {
					t.Fatal("wrong redelivered Job")
				}
				if err := consumer.Delete(ctx, delivery.ReceiptHandle); err != nil {
					t.Fatal(err)
				}
			} else {
				if stored.Status != job.StatusCompleted || stored.ResultKey == nil {
					t.Fatal("Job did not complete")
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(*stored.ResultKey))); err != nil {
					t.Fatal(err)
				}
				if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
					t.Fatalf("successful message was not deleted: %v", err)
				}
			}
			if extender.calls.Load() != before {
				t.Fatal("heartbeat continued after completion")
			}
		})
	}
}
