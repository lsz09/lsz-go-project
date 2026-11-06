package sqsqueue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cloudqueue/internal/jobqueue"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type visibilityClient struct {
	*fakeClient
	change func(context.Context, *sqs.ChangeMessageVisibilityInput) (*sqs.ChangeMessageVisibilityOutput, error)
}

func (c *visibilityClient) ChangeMessageVisibility(ctx context.Context, input *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	var options sqs.Options
	for _, opt := range opts {
		opt(&options)
	}
	if options.RetryMaxAttempts != 1 {
		panic("visibility request unexpectedly retries")
	}
	return c.change(ctx, input)
}

func TestExtendVisibilityForwardsAndSanitizes(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	cause := errors.New("receipt-secret credential-secret")
	called := 0
	client := &visibilityClient{fakeClient: completeFakeClient(), change: func(received context.Context, input *sqs.ChangeMessageVisibilityInput) (*sqs.ChangeMessageVisibilityOutput, error) {
		called++
		if received != ctx || aws.ToString(input.QueueUrl) != testQueueURL || aws.ToString(input.ReceiptHandle) != "receipt-secret" || input.VisibilityTimeout != 60 {
			t.Error("visibility request changed")
		}
		if called == 1 {
			return &sqs.ChangeMessageVisibilityOutput{}, nil
		}
		return nil, cause
	}}
	consumer, err := NewConsumer(client, testQueueURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ExtendVisibility(ctx, "receipt-secret", 60); err != nil {
		t.Fatal(err)
	}
	err = consumer.ExtendVisibility(ctx, "receipt-secret", 60)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe or lost SDK cause: %v", err)
	}
}

func TestExtendVisibilityRejectsInvalidInput(t *testing.T) {
	calls := 0
	client := &visibilityClient{fakeClient: completeFakeClient(), change: func(context.Context, *sqs.ChangeMessageVisibilityInput) (*sqs.ChangeMessageVisibilityOutput, error) {
		calls++
		return nil, nil
	}}
	consumer, err := NewConsumer(client, testQueueURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"", "   "} {
		if err := consumer.ExtendVisibility(context.Background(), handle, 60); !errors.Is(err, jobqueue.ErrInvalidDelivery) {
			t.Fatal(err)
		}
	}
	for _, seconds := range []int32{-1, 0, 43201} {
		if err := consumer.ExtendVisibility(context.Background(), "receipt", seconds); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatal(err)
		}
	}
	if err := consumer.ExtendVisibility(nil, "receipt", 60); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := consumer.ExtendVisibility(ctx, "receipt", 60); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("SDK called for invalid input")
	}
	var missing *Consumer
	if err := missing.ExtendVisibility(context.Background(), "receipt", 60); err == nil {
		t.Fatal("nil consumer accepted")
	}
}
