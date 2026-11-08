package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"cloudqueue/internal/database"
	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue/sqsqueue"
	"cloudqueue/internal/loganalysis"
	"cloudqueue/internal/logprocessor"
	"cloudqueue/internal/storage"
	workerpkg "cloudqueue/internal/worker"
)

const startupTimeout = 10 * time.Second

// configuration contains the command input without exposing environment values in errors.
type configuration struct {
	jobID            string
	databaseURL      string
	localStorageRoot string
	sqs              *sqsqueue.Config
	visibility       workerpkg.VisibilityConfig
	storage          storage.Config
}

// JobProcessor is the smallest Worker behavior required by the command.
type JobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
}

// safeOperationError preserves a cause for inspection while keeping secrets out of logs.
type safeOperationError struct {
	operation string
	cause     error
}

func (e *safeOperationError) Error() string {
	return e.operation
}

func (e *safeOperationError) Unwrap() error {
	return e.cause
}

func main() {
	os.Exit(command(os.Args[1:], os.Getenv))
}

// command owns process-level setup and converts failures into a process exit code.
func command(args []string, getenv func(string) string) int {
	if err := godotenv.Load(); err != nil {
		slog.Info(".env file was not loaded; using process environment", "error", err)
	}

	config, err := loadConfiguration(args, getenv, os.LookupEnv)
	if err != nil {
		slog.Error("worker configuration is invalid", "error", err)
		return 1
	}

	ctx, stop := newSignalContext(context.Background())
	defer stop()

	if err := execute(ctx, config); err != nil {
		if config.sqs == nil {
			slog.Error("worker job processing failed", "job_id", config.jobID, "error", err)
		} else {
			slog.Error("queue worker stopped with an error", "error", err)
		}
		return 1
	}

	if config.sqs == nil {
		slog.Info("worker job processing completed", "job_id", config.jobID)
	} else {
		slog.Info("queue worker stopped")
	}
	return 0
}

// loadConfiguration validates CLI arguments and the environment without returning secrets.
func loadConfiguration(args []string, getenv func(string) string, lookups ...func(string) (string, bool)) (configuration, error) {
	if getenv == nil {
		return configuration{}, errors.New("load configuration: environment lookup is required")
	}

	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jobID := flags.String("job-id", "", "job UUID to process")
	if err := flags.Parse(args); err != nil {
		return configuration{}, fmt.Errorf("load configuration: parse CLI options: %w", err)
	}
	if flags.NArg() != 0 {
		return configuration{}, errors.New("load configuration: positional arguments are not allowed")
	}
	jobIDProvided := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "job-id" {
			jobIDProvided = true
		}
	})
	if jobIDProvided && strings.TrimSpace(*jobID) == "" {
		return configuration{}, errors.New("load configuration: -job-id must not be empty when provided")
	}

	databaseURL := getenv("DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		return configuration{}, errors.New("load configuration: DATABASE_URL is required")
	}
	lookup := func(key string) (string, bool) { value := getenv(key); return value, value != "" }
	if len(lookups) > 0 && lookups[0] != nil {
		lookup = lookups[0]
	}
	storageConfig, err := storage.ConfigFromEnvironment(lookup)
	if err != nil {
		return configuration{}, fmt.Errorf("load configuration: storage: %w", err)
	}

	config := configuration{
		jobID:            *jobID,
		databaseURL:      databaseURL,
		localStorageRoot: storageConfig.LocalRoot,
		storage:          storageConfig,
	}
	if !jobIDProvided {
		sqsConfig, err := sqsqueue.ConfigFromEnvironment(getenv)
		if err != nil {
			return configuration{}, fmt.Errorf("load configuration: queue mode: %w", err)
		}
		config.sqs = &sqsConfig
		config.visibility, err = workerpkg.VisibilityConfigFromEnvironment(lookup)
		if err != nil {
			return configuration{}, fmt.Errorf("load configuration: queue mode: %w", err)
		}
	}
	return config, nil
}

// execute connects to PostgreSQL and runs either one Job or the long-running queue Worker.
func execute(ctx context.Context, config configuration) error {
	if ctx == nil {
		return errors.New("run worker: context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run worker: %w", err)
	}

	startupContext, cancel := context.WithTimeout(ctx, startupTimeout)
	pool, err := database.Open(startupContext, config.databaseURL)
	cancel()
	if err != nil {
		return &safeOperationError{operation: "run worker: connect database", cause: err}
	}
	defer pool.Close()

	slog.Info("connected to PostgreSQL")
	storageContext, storageCancel := context.WithTimeout(ctx, startupTimeout)
	objectStorage, storageErr := storage.New(storageContext, config.storage)
	storageCancel()
	if storageErr != nil {
		return &safeOperationError{operation: "run worker: configure storage", cause: storageErr}
	}
	processor := newJobProcessorWithStorage(pool, objectStorage)
	if config.sqs != nil {
		startupContext, cancel = context.WithTimeout(ctx, startupTimeout)
		client, clientErr := sqsqueue.NewClient(startupContext, *config.sqs)
		cancel()
		if clientErr != nil {
			return &safeOperationError{operation: "run worker: create SQS client", cause: clientErr}
		}
		consumer, consumerErr := sqsqueue.NewConsumer(client, config.sqs.QueueURL, config.sqs.WaitTimeSeconds)
		if consumerErr != nil {
			return &safeOperationError{operation: "run worker: create SQS consumer", cause: consumerErr}
		}

		slog.Info("queue worker started")
		queuedProcessor := workerpkg.NewQueueProcessor(job.NewService(job.NewRepository(pool)), processor)
		runner, runnerErr := workerpkg.NewQueueRunnerWithVisibility(consumer, queuedProcessor, consumer, slog.Default(), config.visibility)
		if runnerErr != nil {
			return &safeOperationError{operation: "run worker: configure visibility", cause: runnerErr}
		}
		if err := runner.Run(ctx); err != nil {
			return err
		}
		return nil
	}

	slog.Info("worker job processing started", "job_id", config.jobID)
	if err := run(ctx, processor, config.jobID); err != nil {
		return err
	}
	return nil
}

// newJobProcessor wires the existing Repository, Service, Processor, and Worker.
func newJobProcessor(pool *pgxpool.Pool, localStorageRoot string) JobProcessor {
	return newJobProcessorWithStorage(pool, logprocessor.NewLocalStorage(localStorageRoot))
}

func newJobProcessorWithStorage(pool *pgxpool.Pool, objectStorage logprocessor.Storage) JobProcessor {
	jobRepository := job.NewRepository(pool)
	jobService := job.NewService(jobRepository)
	analyzer := loganalysis.New()
	processor := logprocessor.New(objectStorage, analyzer)
	return workerpkg.New(jobService, processor)
}

// run forwards the caller's Context and job ID to the one-shot Worker.
func run(ctx context.Context, processor JobProcessor, jobID string) error {
	if ctx == nil {
		return errors.New("run worker: context is required")
	}
	if processor == nil {
		return errors.New("run worker: job processor is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run worker: %w", err)
	}
	if err := processor.ProcessJob(ctx, jobID); err != nil {
		return fmt.Errorf("run worker: process job: %w", err)
	}
	return nil
}

// newSignalContext cancels work when the process receives an interrupt or SIGTERM.
func newSignalContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(signals)
			cancel()
		})
	}

	go func() {
		select {
		case received := <-signals:
			slog.Info("termination signal received", "signal", received.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, stop
}
