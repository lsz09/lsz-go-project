package sqsqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloudqueue/internal/jobqueue"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const MaxVisibilityTimeoutSeconds int32 = 43200

type ChangeVisibilityAPI interface {
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

var _ jobqueue.VisibilityExtender = (*Consumer)(nil)

// ExtendVisibility changes only this delivery's visibility, not the Queue default.
func (c *Consumer) ExtendVisibility(ctx context.Context, handle string, seconds int32) error {
	if c == nil || isNil(c.client) {
		return errors.New("extend SQS visibility: consumer is not configured")
	}
	client, ok := c.client.(ChangeVisibilityAPI)
	if !ok || isNil(client) {
		return errors.New("extend SQS visibility: client does not support visibility changes")
	}
	if ctx == nil {
		return errors.New("extend SQS visibility: context is required")
	}
	if err := ctx.Err(); err != nil {
		return operationError("extend SQS visibility", err)
	}
	if strings.TrimSpace(handle) == "" {
		return fmt.Errorf("extend SQS visibility: %w", jobqueue.ErrInvalidDelivery)
	}
	if seconds < 1 || seconds > MaxVisibilityTimeoutSeconds {
		return configurationError("visibility timeout must be between 1 and 43200 seconds", nil)
	}
	_, err := client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.queueURL), ReceiptHandle: aws.String(handle), VisibilityTimeout: seconds,
	}, func(o *sqs.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		return operationError("extend SQS visibility", err)
	}
	return nil
}
