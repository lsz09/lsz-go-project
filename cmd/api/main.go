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

	startupContext, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	pool, err := database.Open(startupContext, databaseURL)
	cancel()

	if err != nil {
		slog.Error(
			"failed to connect to PostgreSQL",
			"error", err,
		)
		os.Exit(1)
	}

	defer pool.Close()

	slog.Info("connected to PostgreSQL")

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.NewRouter(pool),
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
