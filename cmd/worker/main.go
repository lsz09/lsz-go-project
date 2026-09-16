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
	"cloudqueue/internal/loganalysis"
	"cloudqueue/internal/logprocessor"
	workerpkg "cloudqueue/internal/worker"
)

const startupTimeout = 10 * time.Second

// configuration contains the command input without exposing environment values in errors.
type configuration struct {
	jobID            string
	databaseURL      string
	localStorageRoot string
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

	config, err := loadConfiguration(args, getenv)
	if err != nil {
		slog.Error("worker configuration is invalid", "error", err)
		return 1
	}

	ctx, stop := newSignalContext(context.Background())
	defer stop()

	if err := execute(ctx, config); err != nil {
		slog.Error("worker job processing failed", "job_id", config.jobID, "error", err)
		return 1
	}

	slog.Info("worker job processing completed", "job_id", config.jobID)
	return 0
}

// loadConfiguration validates CLI arguments and the environment without returning secrets.
func loadConfiguration(args []string, getenv func(string) string) (configuration, error) {
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
	if strings.TrimSpace(*jobID) == "" {
		return configuration{}, errors.New("load configuration: -job-id is required")
	}

	databaseURL := getenv("DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		return configuration{}, errors.New("load configuration: DATABASE_URL is required")
	}
	localStorageRoot := getenv("LOCAL_STORAGE_ROOT")
	if strings.TrimSpace(localStorageRoot) == "" {
		return configuration{}, errors.New("load configuration: LOCAL_STORAGE_ROOT is required")
	}

	return configuration{
		jobID:            *jobID,
		databaseURL:      databaseURL,
		localStorageRoot: localStorageRoot,
	}, nil
}

// execute connects to PostgreSQL, assembles the Worker dependencies, and processes one job.
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
	processor := newJobProcessor(pool, config.localStorageRoot)
	slog.Info("worker job processing started", "job_id", config.jobID)
	if err := run(ctx, processor, config.jobID); err != nil {
		return err
	}
	return nil
}

// newJobProcessor wires the existing Repository, Service, Processor, and Worker.
func newJobProcessor(pool *pgxpool.Pool, localStorageRoot string) JobProcessor {
	jobRepository := job.NewRepository(pool)
	jobService := job.NewService(jobRepository)
	localStorage := logprocessor.NewLocalStorage(localStorageRoot)
	analyzer := loganalysis.New()
	processor := logprocessor.New(localStorage, analyzer)
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
