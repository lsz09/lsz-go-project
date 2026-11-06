package worker

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"cloudqueue/internal/jobqueue"
)

type visibilityFunc func(context.Context, string, int32) error

func (f visibilityFunc) ExtendVisibility(ctx context.Context, h string, s int32) error {
	return f(ctx, h, s)
}

func visibilityTestConfig() VisibilityConfig {
	return VisibilityConfig{TimeoutSeconds: 1, HeartbeatInterval: 100 * time.Millisecond, RequestTimeout: 40 * time.Millisecond}
}

func visibilityConsumer(cancel context.CancelFunc, onDelete func()) *runnerConsumer {
	first := true
	return &runnerConsumer{
		receive: func(ctx context.Context) (jobqueue.Delivery, error) {
			if !first {
				<-ctx.Done()
				return jobqueue.Delivery{}, ctx.Err()
			}
			first = false
			return jobqueue.Delivery{Message: jobqueue.Message{JobID: runnerTestJobID}, ReceiptHandle: "receipt-secret"}, nil
		},
		delete: func(context.Context, string) error { onDelete(); cancel(); return nil },
	}
}

func TestVisibilityHeartbeatLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls, active atomic.Int32
		extender := visibilityFunc(func(ctx context.Context, handle string, seconds int32) error {
			if handle != "receipt-secret" || seconds != 1 {
				t.Error("incorrect lease arguments")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("missing request deadline")
			}
			if active.Add(1) != 1 {
				t.Error("overlapping heartbeat")
			}
			defer active.Add(-1)
			calls.Add(1)
			time.Sleep(10 * time.Millisecond)
			return nil
		})
		deleted := false
		consumer := visibilityConsumer(cancel, func() {
			deleted = true
			if active.Load() != 0 {
				t.Error("delete raced active renewal")
			}
		})
		processor := &runnerProcessor{process: func(context.Context, string) error {
			if calls.Load() != 1 {
				t.Error("processor started before initial renewal")
			}
			time.Sleep(350 * time.Millisecond)
			return nil
		}}
		runner, err := NewQueueRunnerWithVisibility(consumer, processor, extender, nil, visibilityTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(ctx); err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		time.Sleep(time.Second)
		synctest.Wait()
		if !deleted || before < 4 || calls.Load() != before || active.Load() != 0 {
			t.Fatalf("renewal lifecycle: deleted=%v calls=%d->%d active=%d", deleted, before, calls.Load(), active.Load())
		}
	})
}

func TestVisibilityInitialFailureDoesNotProcess(t *testing.T) {
	cause := errors.New("credential receipt-secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer := visibilityConsumer(cancel, func() { t.Fatal("deleted after initial failure") })
	processor := &runnerProcessor{process: func(context.Context, string) error { t.Fatal("processed after initial failure"); return nil }}
	runner, err := NewQueueRunnerWithVisibility(consumer, processor, visibilityFunc(func(context.Context, string, int32) error { return cause }), nil, visibilityTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(ctx)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe or lost initial error %v", err)
	}
}

func TestVisibilityFailureCancelsProcessorAndPreservesBothCauses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		leaseCause, processCause := errors.New("provider secret"), errors.New("process secret")
		calls := 0
		extender := visibilityFunc(func(context.Context, string, int32) error {
			calls++
			if calls > 1 {
				return leaseCause
			}
			return nil
		})
		processor := &runnerProcessor{process: func(ctx context.Context, _ string) error { <-ctx.Done(); return errors.Join(processCause, ctx.Err()) }}
		runner, err := NewQueueRunnerWithVisibility(visibilityConsumer(cancel, func() { t.Fatal("deleted failed lease") }), processor, extender, nil, visibilityTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		err = runner.Run(ctx)
		if !errors.Is(err, leaseCause) || !errors.Is(err, processCause) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("lost combined causes %v", err)
		}
		if calls != 2 {
			t.Fatalf("unexpected retry count %d", calls)
		}
	})
}

func TestVisibilityCompletionDuringInflightRenewal(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "intentional cancellation", true: "real failure at completion"}[failure], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				began := make(chan struct{})
				cause := errors.New("provider secret")
				calls := 0
				extender := visibilityFunc(func(ctx context.Context, _ string, _ int32) error {
					calls++
					if calls == 1 {
						return nil
					}
					close(began)
					<-ctx.Done()
					if failure {
						return errors.Join(cause, ctx.Err())
					}
					return ctx.Err()
				})
				deleted := false
				processor := &runnerProcessor{process: func(context.Context, string) error { <-began; return nil }}
				runner, err := NewQueueRunnerWithVisibility(visibilityConsumer(cancel, func() { deleted = true }), processor, extender, nil, visibilityTestConfig())
				if err != nil {
					t.Fatal(err)
				}
				err = runner.Run(ctx)
				if failure {
					if !errors.Is(err, cause) || deleted {
						t.Fatalf("real error lost or acknowledged: %v", err)
					}
				} else if err != nil || !deleted {
					t.Fatalf("normal cancellation treated as failure: %v", err)
				}
			})
		})
	}
}

func TestVisibilityDeadlineAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			extender := visibilityFunc(func(ctx context.Context, _ string, _ int32) error {
				calls++
				if calls == 1 {
					return nil
				}
				<-ctx.Done()
				return ctx.Err()
			})
			processor := &runnerProcessor{process: func(received context.Context, _ string) error {
				if shutdown {
					time.AfterFunc(110*time.Millisecond, cancel)
				}
				<-received.Done()
				return received.Err()
			}}
			runner, err := NewQueueRunnerWithVisibility(visibilityConsumer(cancel, func() { t.Fatal("deleted canceled delivery") }), processor, extender, nil, visibilityTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(ctx)
			if shutdown {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("missing API timeout: %v", err)
			}
			before := calls
			time.Sleep(time.Second)
			synctest.Wait()
			if calls != before {
				t.Fatal("renewal survived shutdown")
			}
		})
	}
}

func TestVisibilityProcessingFailureStopsRenewalAndContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		extender := visibilityFunc(func(context.Context, string, int32) error { calls++; return nil })
		consumer := &runnerConsumer{receive: func(context.Context) (jobqueue.Delivery, error) {
			return jobqueue.Delivery{Message: jobqueue.Message{JobID: runnerTestJobID}, ReceiptHandle: "receipt"}, nil
		}, delete: func(context.Context, string) error { cancel(); return nil }}
		processed := 0
		processor := &runnerProcessor{process: func(context.Context, string) error {
			processed++
			if processed == 1 {
				time.Sleep(150 * time.Millisecond)
				return errors.New("invalid log")
			}
			return nil
		}}
		runner, err := NewQueueRunnerWithVisibility(consumer, processor, extender, nil, visibilityTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if processed != 2 || calls != 3 {
			t.Fatalf("failed delivery not released: processed=%d extensions=%d", processed, calls)
		}
	})
}

func TestVisibilityEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]string
		invalid bool
	}{
		{"defaults", nil, false},
		{"custom", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": "90", "SQS_VISIBILITY_HEARTBEAT_SECONDS": "30"}, false},
		{"empty", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": ""}, true},
		{"zero", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": "0"}, true},
		{"negative", map[string]string{"SQS_VISIBILITY_HEARTBEAT_SECONDS": "-1"}, true},
		{"invalid", map[string]string{"SQS_VISIBILITY_HEARTBEAT_SECONDS": "secret"}, true},
		{"maximum", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": "43200"}, false},
		{"too large", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": "43201"}, true},
		{"equal", map[string]string{"SQS_VISIBILITY_HEARTBEAT_SECONDS": "60"}, true},
		{"longer", map[string]string{"SQS_VISIBILITY_HEARTBEAT_SECONDS": "61"}, true},
		{"insufficient margin", map[string]string{"SQS_VISIBILITY_TIMEOUT_SECONDS": "30", "SQS_VISIBILITY_HEARTBEAT_SECONDS": "20"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := VisibilityConfigFromEnvironment(func(k string) (string, bool) { v, ok := tc.values[k]; return v, ok })
			if (err != nil) != tc.invalid {
				t.Fatalf("configuration %v: %v", config, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("configuration value leaked")
			}
			if tc.values == nil && (config.TimeoutSeconds != 60 || config.HeartbeatInterval != 20*time.Second || config.RequestTimeout != 5*time.Second) {
				t.Fatal("unexpected defaults")
			}
		})
	}
}
