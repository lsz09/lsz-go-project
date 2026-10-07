package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"cloudqueue/internal/database"
	"cloudqueue/internal/httpapi"
	"cloudqueue/internal/job"
	"cloudqueue/internal/jobqueue/sqsqueue"
	"cloudqueue/internal/jobsubmission"
)

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Info(
			".env file was not loaded; using process environment",
			"error", err,
		)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	sqsConfig, err := sqsqueue.ConfigFromEnvironment(os.Getenv)
	if err != nil {
		slog.Error("invalid SQS configuration", "error", err)
		os.Exit(1)
	}

	startupContext, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	pool, err := database.Open(startupContext, databaseURL)

	if err != nil {
		cancel()
		slog.Error(
			"failed to connect to PostgreSQL",
			"error", err,
		)
		os.Exit(1)
	}

	defer pool.Close()

	slog.Info("connected to PostgreSQL")

	sqsClient, err := sqsqueue.NewClient(startupContext, sqsConfig)
	cancel()
	if err != nil {
		slog.Error("failed to configure SQS client", "error", err)
		os.Exit(1)
	}
	sqsPublisher, err := sqsqueue.NewPublisher(sqsClient, sqsConfig.QueueURL)
	if err != nil {
		slog.Error("failed to configure SQS publisher", "error", err)
		os.Exit(1)
	}
	slog.Info("configured SQS publisher")

	// Job 저장과 SQS 발행을 조정하는 Submission Service를 Handler에 주입합니다.
	jobRepository := job.NewRepository(pool)
	jobService := job.NewService(jobRepository)
	jobSubmissionService := jobsubmission.NewService(jobService, sqsPublisher)
	jobHandler := job.NewHandler(jobSubmissionService)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.NewRouter(pool, jobHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info(
		"starting API server",
		"port", port,
	)

	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error(
			"API server stopped unexpectedly",
			"error", err,
		)
		os.Exit(1)
	}
}
