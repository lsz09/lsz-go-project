package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"cloudqueue/internal/jobqueue"
)

const visibilityRequestTimeout = 5 * time.Second

// VisibilityConfig leaves a full request budget plus a scheduling margin before expiry.
type VisibilityConfig struct {
	TimeoutSeconds    int32
	HeartbeatInterval time.Duration
	RequestTimeout    time.Duration
}

func (c VisibilityConfig) Validate() error {
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 43200 {
		return errors.New("visibility timeout must be between 1 and 43200 seconds")
	}
	lease := time.Duration(c.TimeoutSeconds) * time.Second
	if c.HeartbeatInterval <= 0 || c.RequestTimeout <= 0 || c.HeartbeatInterval >= lease || c.RequestTimeout >= (lease-c.HeartbeatInterval)/2 {
		return errors.New("visibility heartbeat and request budget require more expiry margin")
	}
	return nil
}

// VisibilityConfigFromEnvironment distinguishes missing values from explicitly empty values.
func VisibilityConfigFromEnvironment(lookup func(string) (string, bool)) (VisibilityConfig, error) {
	if lookup == nil {
		return VisibilityConfig{}, errors.New("visibility configuration: environment lookup is required")
	}
	values := []int64{60, 20}
	for i, key := range []string{"SQS_VISIBILITY_TIMEOUT_SECONDS", "SQS_VISIBILITY_HEARTBEAT_SECONDS"} {
		if raw, exists := lookup(key); exists {
			value, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || value <= 0 {
				return VisibilityConfig{}, fmt.Errorf("visibility configuration: %s must be a positive integer", key)
			}
			values[i] = value
		}
	}
	config := VisibilityConfig{TimeoutSeconds: int32(values[0]), HeartbeatInterval: time.Duration(values[1]) * time.Second, RequestTimeout: visibilityRequestTimeout}
	return config, config.Validate()
}

// NewQueueRunnerWithVisibility enables lease renewal for every valid delivery.
func NewQueueRunnerWithVisibility(consumer jobqueue.Consumer, processor JobProcessor, extender jobqueue.VisibilityExtender, logger *slog.Logger, config VisibilityConfig) (*QueueRunner, error) {
	if consumer == nil || processor == nil || extender == nil {
		return nil, errors.New("queue visibility: dependencies are required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	runner := NewQueueRunner(consumer, processor, logger)
	runner.extender, runner.visibility = extender, &config
	return runner, nil
}

func (r *QueueRunner) extend(ctx context.Context, handle string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, r.visibility.RequestTimeout)
	defer cancel()
	return r.extender.ExtendVisibility(requestContext, handle, r.visibility.TimeoutSeconds)
}

// processDelivery runs the processor synchronously and joins the renewal goroutine
// before returning. Its second result is a fatal lease failure, never acknowledged.
func (r *QueueRunner) processDelivery(ctx context.Context, delivery jobqueue.Delivery) (error, error) {
	if r.visibility == nil {
		return r.processor.ProcessJob(ctx, delivery.Message.JobID), nil
	}
	if err := r.extend(ctx, delivery.ReceiptHandle); err != nil {
		return nil, &runnerOperationError{operation: "queue worker: initial visibility", cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err, nil
	}
	processContext, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	renewContext, stopRenew := context.WithCancel(ctx)
	defer stopRenew()
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(r.visibility.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewContext.Done():
				done <- nil
				return
			case <-ticker.C:
				if renewContext.Err() != nil {
					done <- nil
					return
				}
				if err := r.extend(renewContext, delivery.ReceiptHandle); err != nil {
					// Only our deliberate shutdown cancellation is benign. An API
					// deadline or provider error remains fatal even if processing just ended.
					if cancellationOnly(err) && renewContext.Err() != nil {
						done <- nil
						return
					}
					cancelProcess()
					done <- &runnerOperationError{operation: "queue worker: renew visibility", cause: err}
					return
				}
			}
		}
	}()
	processErr := r.processor.ProcessJob(processContext, delivery.Message.JobID)
	stopRenew()
	renewErr := <-done
	if renewErr != nil {
		return nil, &runnerOperationError{operation: "queue worker: visibility renewal failed", cause: errors.Join(renewErr, processErr)}
	}
	return processErr, nil
}

// A joined provider failure must not be hidden just because it also contains
// context.Canceled. SDK wrapper chains around cancellation alone are benign.
func cancellationOnly(err error) bool {
	if err == context.Canceled {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return cancellationOnly(wrapped.Unwrap())
	default:
		return false
	}
}
