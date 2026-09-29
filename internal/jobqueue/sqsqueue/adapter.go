package sqsqueue

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"cloudqueue/internal/jobqueue"
)

var ErrInvalidDelivery = errors.New("invalid SQS delivery")

// SendMessageAPI is the minimal AWS SDK surface required by Publisher.
type SendMessageAPI interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// ConsumerAPI is the minimal AWS SDK surface required by Consumer.
type ConsumerAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// Publisher sends validated Job messages to one SQS queue.
type Publisher struct {
	client   SendMessageAPI
	queueURL string
}

// Consumer receives and deletes Job messages from one SQS queue.
type Consumer struct {
	client          ConsumerAPI
	queueURL        string
	waitTimeSeconds int32
}

var (
	_ jobqueue.Publisher = (*Publisher)(nil)
	_ jobqueue.Consumer  = (*Consumer)(nil)
)

// NewPublisher creates an SQS Publisher with validated dependencies.
func NewPublisher(client SendMessageAPI, queueURL string) (*Publisher, error) {
	if isNil(client) {
		return nil, errors.New("create SQS publisher: client is required")
	}
	if err := validateHTTPURL(queueURL); err != nil {
		return nil, configurationError("SQS_QUEUE_URL must be an absolute HTTP(S) URL", err)
	}
	return &Publisher{client: client, queueURL: queueURL}, nil
}

// NewConsumer creates an SQS Consumer with validated dependencies.
func NewConsumer(client ConsumerAPI, queueURL string, waitTimeSeconds int32) (*Consumer, error) {
	if isNil(client) {
		return nil, errors.New("create SQS consumer: client is required")
	}
	if err := validateHTTPURL(queueURL); err != nil {
		return nil, configurationError("SQS_QUEUE_URL must be an absolute HTTP(S) URL", err)
	}
	if waitTimeSeconds < 0 || waitTimeSeconds > MaxWaitTimeSeconds {
		return nil, configurationError("SQS_WAIT_TIME_SECONDS must be between 0 and 20", nil)
	}
	return &Consumer{client: client, queueURL: queueURL, waitTimeSeconds: waitTimeSeconds}, nil
}

// Publish encodes a Job message and sends it to SQS.
func (p *Publisher) Publish(ctx context.Context, message jobqueue.Message) error {
	if p == nil || isNil(p.client) {
		return errors.New("publish SQS message: publisher is not configured")
	}
	if ctx == nil {
		return errors.New("publish SQS message: context is required")
	}
	if err := ctx.Err(); err != nil {
		return operationError("publish SQS message", err)
	}
	body, err := jobqueue.Encode(message)
	if err != nil {
		return fmt.Errorf("publish SQS message: %w", err)
	}
	if _, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(p.queueURL),
		MessageBody: aws.String(string(body)),
	}); err != nil {
		return operationError("publish SQS message", err)
	}
	return nil
}

// Receive long polls SQS for one message and converts it to a Delivery.
func (c *Consumer) Receive(ctx context.Context) (jobqueue.Delivery, error) {
	if c == nil || isNil(c.client) {
		return jobqueue.Delivery{}, errors.New("receive SQS message: consumer is not configured")
	}
	if ctx == nil {
		return jobqueue.Delivery{}, errors.New("receive SQS message: context is required")
	}
	if err := ctx.Err(); err != nil {
		return jobqueue.Delivery{}, operationError("receive SQS message", err)
	}

	output, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.queueURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     c.waitTimeSeconds,
	})
	if err != nil {
		return jobqueue.Delivery{}, operationError("receive SQS message", err)
	}
	if output == nil || len(output.Messages) == 0 {
		return jobqueue.Delivery{}, fmt.Errorf("receive SQS message: %w", jobqueue.ErrNoMessage)
	}
	return decodeDelivery(output.Messages[0])
}

// Delete acknowledges a successfully processed SQS message.
func (c *Consumer) Delete(ctx context.Context, receiptHandle string) error {
	if c == nil || isNil(c.client) {
		return errors.New("delete SQS message: consumer is not configured")
	}
	if ctx == nil {
		return errors.New("delete SQS message: context is required")
	}
	if err := ctx.Err(); err != nil {
		return operationError("delete SQS message", err)
	}
	if strings.TrimSpace(receiptHandle) == "" {
		return fmt.Errorf("delete SQS message: %w: receipt handle is required", ErrInvalidDelivery)
	}
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	}); err != nil {
		return operationError("delete SQS message", err)
	}
	return nil
}

func decodeDelivery(message types.Message) (jobqueue.Delivery, error) {
	decoded, err := jobqueue.Decode([]byte(aws.ToString(message.Body)))
	if err != nil {
		return jobqueue.Delivery{}, fmt.Errorf("decode SQS message: %w", err)
	}
	receiptHandle := aws.ToString(message.ReceiptHandle)
	if strings.TrimSpace(receiptHandle) == "" {
		return jobqueue.Delivery{}, fmt.Errorf("decode SQS message: %w: receipt handle is required", ErrInvalidDelivery)
	}
	return jobqueue.Delivery{Message: decoded, ReceiptHandle: receiptHandle}, nil
}

func operationError(operation string, cause error) error {
	return &safeCause{description: operation, cause: cause}
}

type safeCause struct {
	description string
	cause       error
}

func (e *safeCause) Error() string {
	return e.description
}

func (e *safeCause) Unwrap() error {
	return e.cause
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
