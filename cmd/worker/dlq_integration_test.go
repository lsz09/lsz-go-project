//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue"
	workerpkg "cloudqueue/internal/worker"
)

type countedProcessor struct {
	processor JobProcessor
	calls     atomic.Int32
}

func (p *countedProcessor) ProcessJob(ctx context.Context, id string) error {
	p.calls.Add(1)
	return p.processor.ProcessJob(ctx, id)
}

func TestQueueWorkerRedriveIntegration(t *testing.T) {
	pool := workerIntegrationPool(t)
	client, sourceURL := workerLocalStackQueue(t)
	_, dlqURL := workerLocalStackQueue(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	attributes, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]any{"deadLetterTargetArn": attributes.Attributes["QueueArn"], "maxReceiveCount": 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(sourceURL), Attributes: map[string]string{"RedrivePolicy": string(policy)}}); err != nil {
		t.Fatal(err)
	}
	repository := job.NewRepository(pool)
	root := t.TempDir()
	const valid = "127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n"
	completed := workerCreateJobWithLog(t, repository, root, "completed.log", valid)
	processor := newJobProcessor(pool, root)
	if err := processor.ProcessJob(ctx, completed.ID); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "results", completed.ID+".json")
	before, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	failed := workerCreateJobWithLog(t, repository, root, "failed.log", "invalid log\n")
	good := workerCreateJobWithLog(t, repository, root, "good.log", valid)
	publisher, consumer := workerQueueAdapters(t, client, sourceURL)
	const invalidBody = `{"job_id":"invalid-secret","unexpected":"body-secret"}`
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(sourceURL), MessageBody: aws.String(invalidBody)}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{failed.ID, completed.ID, good.ID} {
		message, err := jobqueue.NewMessage(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Publish(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedProcessor{processor: processor}
	queued := workerpkg.NewQueueProcessor(job.NewService(repository), counted)
	runContext, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- workerpkg.NewQueueRunner(consumer, queued, nil).Run(runContext) }()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runner stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runner did not stop")
		}
	}()
	found := map[string]bool{}
	for len(found) < 2 {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(dlqURL), WaitTimeSeconds: 1, MaxNumberOfMessages: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range output.Messages {
			body := aws.ToString(message.Body)
			if body == invalidBody {
				found["invalid"] = true
			} else {
				decoded, err := jobqueue.Decode([]byte(body))
				if err != nil || decoded.JobID != failed.ID {
					t.Fatalf("unexpected DLQ message: decode=%v", err)
				}
				found["failed"] = true
			}
			if _, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(dlqURL), ReceiptHandle: message.ReceiptHandle}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The live runner must also finish the healthy message before being stopped.
	for {
		stored, err := repository.FindByID(ctx, good.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status == job.StatusCompleted {
			remaining, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
				QueueUrl:       aws.String(sourceURL),
				AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
			})
			if err != nil {
				t.Fatal(err)
			}
			if remaining.Attributes["ApproximateNumberOfMessages"] == "0" && remaining.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0" {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	stored, err := repository.FindByID(ctx, failed.ID)
	if err != nil || stored.Status != job.StatusFailed {
		t.Fatalf("failed Job changed: %+v %v", stored, err)
	}
	if counted.calls.Load() != 2 {
		t.Fatalf("processor executed %d times, want exactly good+failed", counted.calls.Load())
	}
	after, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("duplicate rewrote completed result")
	}
	if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
		t.Fatalf("source still contains message: %v", err)
	}
}
