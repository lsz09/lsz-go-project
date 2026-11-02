//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	"cloudqueue/internal/jobqueue/sqsqueue"
	workerpkg "cloudqueue/internal/worker"
)

type cancelAfterDeleteConsumer struct {
	jobqueue.Consumer
	cancel  context.CancelFunc
	deleted bool
}

func (c *cancelAfterDeleteConsumer) Delete(ctx context.Context, receiptHandle string) error {
	if err := c.Consumer.Delete(ctx, receiptHandle); err != nil {
		return err
	}
	c.deleted = true
	c.cancel()
	return nil
}

type cancelAfterProcess struct {
	processor JobProcessor
	cancel    context.CancelFunc
}

func (p *cancelAfterProcess) ProcessJob(ctx context.Context, jobID string) error {
	err := p.processor.ProcessJob(ctx, jobID)
	p.cancel()
	return err
}

func TestQueueWorkerIntegration(t *testing.T) {
	t.Run("completes Job and deletes successful message", func(t *testing.T) {
		pool := workerIntegrationPool(t)
		client, queueURL := workerLocalStackQueue(t)
		publisher, consumer := workerQueueAdapters(t, client, queueURL)
		repository := job.NewRepository(pool)
		storageRoot := t.TempDir()
		created := workerCreateJobWithLog(t, repository, storageRoot, "valid.log",
			"127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n")
		message, err := jobqueue.NewMessage(created.ID)
		if err != nil {
			t.Fatalf("new message: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := publisher.Publish(ctx, message); err != nil {
			t.Fatalf("publish: %v", err)
		}

		runContext, stop := context.WithCancel(ctx)
		cancelingConsumer := &cancelAfterDeleteConsumer{Consumer: consumer, cancel: stop}
		if err := workerpkg.NewQueueRunner(cancelingConsumer, newJobProcessor(pool, storageRoot), nil).Run(runContext); err != nil {
			t.Fatalf("run queue worker: %v", err)
		}
		if !cancelingConsumer.deleted {
			t.Fatal("successful message was not deleted")
		}

		stored, err := repository.FindByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("find completed Job: %v", err)
		}
		if stored.Status != job.StatusCompleted || stored.ResultKey == nil {
			t.Fatalf("unexpected completed Job: %+v", stored)
		}
		if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
			t.Fatalf("expected successful message to be absent, got %v", err)
		}
	})

	t.Run("marks Job failed and leaves message for retry", func(t *testing.T) {
		pool := workerIntegrationPool(t)
		client, queueURL := workerLocalStackQueue(t)
		publisher, consumer := workerQueueAdapters(t, client, queueURL)
		repository := job.NewRepository(pool)
		storageRoot := t.TempDir()
		created := workerCreateJobWithLog(t, repository, storageRoot, "invalid.log", "not a combined log line\n")
		message, err := jobqueue.NewMessage(created.ID)
		if err != nil {
			t.Fatalf("new message: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := publisher.Publish(ctx, message); err != nil {
			t.Fatalf("publish: %v", err)
		}

		runContext, stop := context.WithCancel(ctx)
		processor := &cancelAfterProcess{processor: newJobProcessor(pool, storageRoot), cancel: stop}
		if err := workerpkg.NewQueueRunner(consumer, processor, nil).Run(runContext); err != nil {
			t.Fatalf("run queue worker: %v", err)
		}
		stored, err := repository.FindByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("find failed Job: %v", err)
		}
		if stored.Status != job.StatusFailed || stored.ErrorMessage == nil {
			t.Fatalf("unexpected failed Job: %+v", stored)
		}

		delivery := workerReceiveVisibleMessage(t, ctx, consumer)
		if delivery.Message.JobID != created.ID {
			t.Fatalf("retried Job ID = %q, want %q", delivery.Message.JobID, created.ID)
		}
		if err := consumer.Delete(ctx, delivery.ReceiptHandle); err != nil {
			t.Fatalf("delete retained message during cleanup: %v", err)
		}
	})
}

func workerLocalStackQueue(t *testing.T) (*sqs.Client, string) {
	t.Helper()
	endpointURL := os.Getenv("TEST_SQS_ENDPOINT_URL")
	if endpointURL == "" {
		t.Fatal("integration tests require TEST_SQS_ENDPOINT_URL pointing to LocalStack")
	}
	region := os.Getenv("TEST_AWS_REGION")
	if region == "" {
		region = "ap-northeast-2"
	}
	config := sqsqueue.Config{
		Region:          region,
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		EndpointURL:     endpointURL,
		QueueURL:        endpointURL,
		WaitTimeSeconds: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := sqsqueue.NewClient(ctx, config)
	if err != nil {
		t.Fatalf("new LocalStack client: %v", err)
	}
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(fmt.Sprintf("cloudqueue-worker-test-%d", time.Now().UnixNano())),
		Attributes: map[string]string{
			string(types.QueueAttributeNameVisibilityTimeout): "1",
		},
	})
	if err != nil {
		t.Fatalf("create test queue: %v", err)
	}
	queueURL := aws.ToString(created.QueueUrl)
	if queueURL == "" {
		t.Fatal("LocalStack returned an empty queue URL")
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := client.DeleteQueue(cleanupContext, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
			t.Errorf("delete test queue: %v", err)
		}
	})
	return client, queueURL
}

func workerQueueAdapters(t *testing.T, client *sqs.Client, queueURL string) (*sqsqueue.Publisher, *sqsqueue.Consumer) {
	t.Helper()
	publisher, err := sqsqueue.NewPublisher(client, queueURL)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	consumer, err := sqsqueue.NewConsumer(client, queueURL, 1)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	return publisher, consumer
}

func workerCreateJobWithLog(t *testing.T, repository *job.Repository, storageRoot, fileName, content string) job.Job {
	t.Helper()
	fileKey := "uploads/" + fileName
	created, err := repository.Create(context.Background(), job.CreateParams{FileName: fileName, FileKey: &fileKey})
	if err != nil {
		t.Fatalf("create Job: %v", err)
	}
	inputPath := filepath.Join(storageRoot, filepath.FromSlash(fileKey))
	if err := os.MkdirAll(filepath.Dir(inputPath), 0o750); err != nil {
		t.Fatalf("create input directory: %v", err)
	}
	if err := os.WriteFile(inputPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write input log: %v", err)
	}
	return created
}

func workerReceiveVisibleMessage(t *testing.T, ctx context.Context, consumer *sqsqueue.Consumer) jobqueue.Delivery {
	t.Helper()
	for {
		delivery, err := consumer.Receive(ctx)
		if err == nil {
			return delivery
		}
		if !errors.Is(err, jobqueue.ErrNoMessage) {
			t.Fatalf("receive retained message: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatalf("wait for retained message: %v", ctx.Err())
		}
	}
}
