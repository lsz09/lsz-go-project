//go:build integration

package sqsqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"cloudqueue/internal/jobqueue"
)

func TestLocalStackPublishReceiveDelete(t *testing.T) {
	client, queueURL := localStackQueue(t)
	publisher, err := NewPublisher(client, queueURL)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	consumer, err := NewConsumer(client, queueURL, 1)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	message, err := jobqueue.NewMessage(testJobID)
	if err != nil {
		t.Fatalf("new message: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatalf("publish: %v", err)
	}
	delivery, err := consumer.Receive(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if delivery.Message != message || delivery.ReceiptHandle == "" {
		t.Fatalf("unexpected delivery: %+v", delivery)
	}
	if err := consumer.Delete(ctx, delivery.ReceiptHandle); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrNoMessage) {
		t.Fatalf("expected deleted queue to be empty, got %v", err)
	}
}

func TestLocalStackRejectsInvalidMessageBody(t *testing.T) {
	client, queueURL := localStackQueue(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(`{"job_id":"invalid"}`),
	}); err != nil {
		t.Fatalf("send invalid message: %v", err)
	}
	consumer, err := NewConsumer(client, queueURL, 1)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	if _, err := consumer.Receive(ctx); !errors.Is(err, jobqueue.ErrInvalidMessage) {
		t.Fatalf("expected invalid message error, got %v", err)
	}
}

func TestLocalStackLongPollingHonorsContext(t *testing.T) {
	client, queueURL := localStackQueue(t)
	consumer, err := NewConsumer(client, queueURL, MaxWaitTimeSeconds)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := consumer.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func localStackQueue(t *testing.T) (*sqs.Client, string) {
	t.Helper()
	endpointURL := os.Getenv("TEST_SQS_ENDPOINT_URL")
	if endpointURL == "" {
		t.Fatal("integration tests require TEST_SQS_ENDPOINT_URL pointing to LocalStack")
	}
	region := os.Getenv("TEST_AWS_REGION")
	if region == "" {
		region = "ap-northeast-2"
	}
	config := Config{
		Region:          region,
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		EndpointURL:     endpointURL,
		QueueURL:        endpointURL,
		WaitTimeSeconds: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := NewClient(ctx, config)
	if err != nil {
		t.Fatalf("new LocalStack client: %v", err)
	}
	queueName := fmt.Sprintf("cloudqueue-test-%d", time.Now().UnixNano())
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
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
