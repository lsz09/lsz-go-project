package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"cloudqueue/internal/httpapi"
)

func main() {
	port := os.Getenv("RORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info(
		"starting CloudQueue API server",
		"port", port,
	)

	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error(
			"API server stopped unexpectedly",
			"error", err,
		)
		os.Exit(1)
	}
}
