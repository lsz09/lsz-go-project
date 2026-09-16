package jobqueue

import "context"

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
