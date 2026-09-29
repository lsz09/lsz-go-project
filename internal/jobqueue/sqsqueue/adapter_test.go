package sqsqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"cloudqueue/internal/jobqueue"
)

const (
	testQueueURL = "http://localhost:4566/000000000000/cloudqueue-jobs"
	testJobID    = "00000000-0000-0000-0000-000000000001"
)

type fakeClient struct {
	send    func(context.Context, *sqs.SendMessageInput) (*sqs.SendMessageOutput, error)
	receive func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error)
	delete  func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error)
}

func (f *fakeClient) SendMessage(ctx context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return f.send(ctx, input)
}

func (f *fakeClient) ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return f.receive(ctx, input)
}

func (f *fakeClient) DeleteMessage(ctx context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return f.delete(ctx, input)
}

func TestConstructors(t *testing.T) {
	client := completeFakeClient()
	if publisher, err := NewPublisher(client, testQueueURL); err != nil || publisher == nil {
		t.Fatalf("new publisher: %v", err)
	}
	if consumer, err := NewConsumer(client, testQueueURL, 20); err != nil || consumer == nil {
		t.Fatalf("new consumer: %v", err)
	}
	if _, err := NewPublisher(nil, testQueueURL); err == nil {
		t.Fatal("expected nil publisher client error")
	}
	if _, err := NewConsumer(nil, testQueueURL, 10); err == nil {
		t.Fatal("expected nil consumer client error")
	}
	if _, err := NewPublisher(client, "not-a-url"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("expected invalid publisher URL, got %v", err)
	}
	if _, err := NewConsumer(client, testQueueURL, 21); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("expected invalid consumer wait time, got %v", err)
	}
}

func TestPublisherPublish(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	called := 0
	client := completeFakeClient()
	client.send = func(received context.Context, input *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
		called++
		if received != ctx {
			t.Fatal("publisher changed context")
		}
		if aws.ToString(input.QueueUrl) != testQueueURL {
			t.Fatalf("unexpected queue URL: %q", aws.ToString(input.QueueUrl))
		}
		if aws.ToString(input.MessageBody) != `{"job_id":"`+testJobID+`"}` {
			t.Fatalf("unexpected body: %q", aws.ToString(input.MessageBody))
		}
		return &sqs.SendMessageOutput{}, nil
	}
	publisher, _ := NewPublisher(client, testQueueURL)
	if err := publisher.Publish(ctx, jobqueue.Message{JobID: testJobID}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected one SDK call, got %d", called)
	}
}

func TestPublisherRejectsBeforeSDKCall(t *testing.T) {
	client := completeFakeClient()
	called := false
	client.send = func(context.Context, *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
		called = true
		return nil, nil
	}
	publisher, _ := NewPublisher(client, testQueueURL)

	if err := publisher.Publish(nil, jobqueue.Message{JobID: testJobID}); err == nil {
		t.Fatal("expected nil context error")
	}
	if err := publisher.Publish(context.Background(), jobqueue.Message{}); !errors.Is(err, jobqueue.ErrInvalidMessage) {
		t.Fatalf("expected invalid message error, got %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publisher.Publish(canceled, jobqueue.Message{JobID: testJobID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
	if called {
		t.Fatal("SDK was called for invalid input")
	}
}

func TestPublisherPreservesSDKErrorWithoutExposingValues(t *testing.T) {
	cause := errors.New("super-secret-test-value body-secret")
	client := completeFakeClient()
	client.send = func(context.Context, *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) { return nil, cause }
	publisher, _ := NewPublisher(client, testQueueURL)
	err := publisher.Publish(context.Background(), jobqueue.Message{JobID: testJobID})
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "super-secret-test-value") || strings.Contains(err.Error(), "body-secret") {
		t.Fatalf("unsafe SDK error: %v", err)
	}
}

func TestConsumerReceive(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	client := completeFakeClient()
	client.receive = func(received context.Context, input *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		if received != ctx {
			t.Fatal("consumer changed context")
		}
		if aws.ToString(input.QueueUrl) != testQueueURL || input.MaxNumberOfMessages != 1 || input.WaitTimeSeconds != 10 {
			t.Fatalf("unexpected receive input: %+v", input)
		}
		return &sqs.ReceiveMessageOutput{Messages: []types.Message{{
			Body:          aws.String(`{"job_id":"` + testJobID + `"}`),
			ReceiptHandle: aws.String("receipt-secret-value"),
		}}}, nil
	}
	consumer, _ := NewConsumer(client, testQueueURL, 10)
	delivery, err := consumer.Receive(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if delivery.Message.JobID != testJobID || delivery.ReceiptHandle != "receipt-secret-value" {
		t.Fatalf("unexpected delivery: %+v", delivery)
	}
}

func TestConsumerReceiveErrors(t *testing.T) {
	tests := []struct {
		name   string
		output *sqs.ReceiveMessageOutput
		cause  error
		want   error
	}{
		{name: "nil output", want: jobqueue.ErrNoMessage},
		{name: "no messages", output: &sqs.ReceiveMessageOutput{}, want: jobqueue.ErrNoMessage},
		{name: "missing body", output: messageOutput(nil, aws.String("receipt-secret-value")), want: jobqueue.ErrInvalidMessage},
		{name: "invalid JSON", output: messageOutput(aws.String(`{"job_id":`), aws.String("receipt-secret-value")), want: jobqueue.ErrInvalidMessage},
		{name: "invalid job ID", output: messageOutput(aws.String(`{"job_id":"invalid"}`), aws.String("receipt-secret-value")), want: jobqueue.ErrInvalidMessage},
		{name: "missing receipt", output: messageOutput(aws.String(`{"job_id":"`+testJobID+`"}`), nil), want: ErrInvalidDelivery},
		{name: "blank receipt", output: messageOutput(aws.String(`{"job_id":"`+testJobID+`"}`), aws.String("   ")), want: ErrInvalidDelivery},
		{name: "SDK failure", cause: errors.New("body-secret receipt-secret-value"), want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := completeFakeClient()
			client.receive = func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
				return test.output, test.cause
			}
			consumer, _ := NewConsumer(client, testQueueURL, 10)
			_, err := consumer.Receive(context.Background())
			if test.cause != nil {
				if !errors.Is(err, test.cause) {
					t.Fatalf("expected SDK cause, got %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if strings.Contains(err.Error(), "body-secret") || strings.Contains(err.Error(), "receipt-secret-value") {
				t.Fatalf("receive error exposed message data: %v", err)
			}
		})
	}
}

func TestConsumerRejectsCanceledReceiveBeforeSDKCall(t *testing.T) {
	client := completeFakeClient()
	called := false
	client.receive = func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		called = true
		return nil, nil
	}
	consumer, _ := NewConsumer(client, testQueueURL, 10)
	if _, err := consumer.Receive(nil); err == nil {
		t.Fatal("expected nil context error")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := consumer.Receive(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if called {
		t.Fatal("SDK was called with invalid context")
	}
}

func TestConsumerDelete(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	client := completeFakeClient()
	called := 0
	client.delete = func(received context.Context, input *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		called++
		if received != ctx || aws.ToString(input.QueueUrl) != testQueueURL || aws.ToString(input.ReceiptHandle) != "receipt-secret-value" {
			t.Fatalf("unexpected delete input: %+v", input)
		}
		return &sqs.DeleteMessageOutput{}, nil
	}
	consumer, _ := NewConsumer(client, testQueueURL, 10)
	if err := consumer.Delete(ctx, "receipt-secret-value"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected one delete call, got %d", called)
	}
}

func TestConsumerDeleteErrors(t *testing.T) {
	client := completeFakeClient()
	called := false
	cause := errors.New("receipt-secret-value")
	client.delete = func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		called = true
		return nil, cause
	}
	consumer, _ := NewConsumer(client, testQueueURL, 10)
	for _, handle := range []string{"", "   "} {
		if err := consumer.Delete(context.Background(), handle); !errors.Is(err, ErrInvalidDelivery) {
			t.Fatalf("expected invalid delivery for %q, got %v", handle, err)
		}
	}
	if err := consumer.Delete(nil, "receipt-secret-value"); err == nil {
		t.Fatal("expected nil context error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := consumer.Delete(canceled, "receipt-secret-value"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
	if called {
		t.Fatal("SDK was called for invalid delete input")
	}

	err := consumer.Delete(context.Background(), "receipt-secret-value")
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "receipt-secret-value") {
		t.Fatalf("unsafe delete error: %v", err)
	}
}

func completeFakeClient() *fakeClient {
	return &fakeClient{
		send: func(context.Context, *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
			return &sqs.SendMessageOutput{}, nil
		},
		receive: func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
			return &sqs.ReceiveMessageOutput{}, nil
		},
		delete: func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
			return &sqs.DeleteMessageOutput{}, nil
		},
	}
}

func messageOutput(body *string, receipt *string) *sqs.ReceiveMessageOutput {
	return &sqs.ReceiveMessageOutput{Messages: []types.Message{{Body: body, ReceiptHandle: receipt}}}
}
