package jobqueue

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePublisher struct {
	publish func(context.Context, Message) error
}

func (f *fakePublisher) Publish(ctx context.Context, message Message) error {
	return f.publish(ctx, message)
}

type fakeConsumer struct {
	receive func(context.Context) (Delivery, error)
	delete  func(context.Context, string) error
}

func (f *fakeConsumer) Receive(ctx context.Context) (Delivery, error) {
	return f.receive(ctx)
}

func (f *fakeConsumer) Delete(ctx context.Context, receiptHandle string) error {
	return f.delete(ctx, receiptHandle)
}

var (
	_ Publisher = (*fakePublisher)(nil)
	_ Consumer  = (*fakeConsumer)(nil)
)

func TestQueueContractsPreserveContextAndValues(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "caller")
	message := Message{JobID: validJobID}
	delivery := Delivery{Message: message, ReceiptHandle: "receipt-1"}

	publisher := &fakePublisher{publish: func(received context.Context, got Message) error {
		if received != ctx || got != message {
			t.Fatalf("publisher changed input: %v, %+v", received, got)
		}
		return nil
	}}
	consumer := &fakeConsumer{
		receive: func(received context.Context) (Delivery, error) {
			if received != ctx {
				t.Fatal("consumer changed receive context")
			}
			return delivery, nil
		},
		delete: func(received context.Context, receiptHandle string) error {
			if received != ctx || receiptHandle != delivery.ReceiptHandle {
				t.Fatalf("consumer changed delete input: %v, %q", received, receiptHandle)
			}
			return nil
		},
	}

	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatalf("publish: %v", err)
	}
	received, err := consumer.Receive(ctx)
	if err != nil || received != delivery {
		t.Fatalf("receive: %+v, %v", received, err)
	}
	if err := consumer.Delete(ctx, received.ReceiptHandle); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestQueueContractImplementationsCanPreserveContextErrors(t *testing.T) {
	tests := []struct {
		name  string
		ctx   context.Context
		cause error
	}{
		{name: "canceled", ctx: canceledContext(), cause: context.Canceled},
		{name: "deadline", ctx: expiredContext(), cause: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &fakePublisher{publish: func(ctx context.Context, _ Message) error { return ctx.Err() }}
			consumer := &fakeConsumer{
				receive: func(ctx context.Context) (Delivery, error) { return Delivery{}, ctx.Err() },
				delete:  func(ctx context.Context, _ string) error { return ctx.Err() },
			}
			if err := publisher.Publish(test.ctx, Message{JobID: validJobID}); !errors.Is(err, test.cause) {
				t.Fatalf("publish lost context error: %v", err)
			}
			if _, err := consumer.Receive(test.ctx); !errors.Is(err, test.cause) {
				t.Fatalf("receive lost context error: %v", err)
			}
			if err := consumer.Delete(test.ctx, "receipt"); !errors.Is(err, test.cause) {
				t.Fatalf("delete lost context error: %v", err)
			}
		})
	}
}

func TestMessageIsDeletedOnlyAfterSuccessfulProcessing(t *testing.T) {
	processError := errors.New("processing failed")
	for _, test := range []struct {
		name       string
		processErr error
		wantDelete bool
	}{
		{name: "success", wantDelete: true},
		{name: "failure", processErr: processError},
	} {
		t.Run(test.name, func(t *testing.T) {
			deleted := false
			consumer := &fakeConsumer{
				receive: func(context.Context) (Delivery, error) {
					return Delivery{Message: Message{JobID: validJobID}, ReceiptHandle: "receipt"}, nil
				},
				delete: func(context.Context, string) error {
					deleted = true
					return nil
				},
			}
			delivery, err := consumer.Receive(context.Background())
			if err != nil {
				t.Fatalf("receive: %v", err)
			}
			if test.processErr == nil {
				if err := consumer.Delete(context.Background(), delivery.ReceiptHandle); err != nil {
					t.Fatalf("delete: %v", err)
				}
			}
			if deleted != test.wantDelete {
				t.Fatalf("delete called = %v, want %v", deleted, test.wantDelete)
			}
		})
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
