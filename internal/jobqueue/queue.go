package jobqueue

import (
	"context"
	"errors"
)

// ErrNoMessage indicates that a queue receive operation completed without a message.
var ErrNoMessage = errors.New("no job queue message")

// ErrInvalidDelivery identifies a received message that must remain unacknowledged.
var ErrInvalidDelivery = errors.New("invalid job queue delivery")

// Delivery contains a validated Message and the opaque token used to acknowledge it.
type Delivery struct {
	Message       Message
	ReceiptHandle string
}

// Publisher sends Job messages without exposing a vendor-specific SDK type.
type Publisher interface {
	Publish(ctx context.Context, message Message) error
}

// Consumer receives Job messages and deletes them only after successful processing.
type Consumer interface {
	Receive(ctx context.Context) (Delivery, error)
	Delete(ctx context.Context, receiptHandle string) error
}

// VisibilityExtender renews the lease for the most recently received delivery.
type VisibilityExtender interface {
	ExtendVisibility(ctx context.Context, receiptHandle string, timeoutSeconds int32) error
}
