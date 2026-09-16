//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cloudqueue/internal/job"
	"cloudqueue/internal/loganalysis"
)

func workerIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("integration tests require TEST_DATABASE_URL pointing to a dedicated test database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(func() {
		closeContext, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := admin.Close(closeContext); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})

	schema := fmt.Sprintf("worker_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	migration, err := os.ReadFile("../../migrations/000001_create_jobs.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}

func TestLocalWorkerIntegration(t *testing.T) {
	pool := workerIntegrationPool(t)
	repository := job.NewRepository(pool)
	storageRoot := t.TempDir()
	processor := newJobProcessor(pool, storageRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("completes valid log", func(t *testing.T) {
		fileKey := "uploads/access.log"
		created, err := repository.Create(ctx, job.CreateParams{FileName: "access.log", FileKey: &fileKey})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		inputPath := filepath.Join(storageRoot, filepath.FromSlash(fileKey))
		if err := os.MkdirAll(filepath.Dir(inputPath), 0o750); err != nil {
			t.Fatalf("create input directory: %v", err)
		}
		logContent := "127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] \"GET /health HTTP/1.1\" 200 42 \"-\" \"Mozilla/5.0\"\n" +
			"127.0.0.1 - - [16/Sep/2026:10:01:00 +0900] \"POST /api/v1/jobs HTTP/1.1\" 500 128 \"-\" \"Mozilla/5.0\"\n"
		if err := os.WriteFile(inputPath, []byte(logContent), 0o600); err != nil {
			t.Fatalf("write input log: %v", err)
		}

		if err := run(ctx, processor, created.ID); err != nil {
			t.Fatalf("run worker: %v", err)
		}
		stored, err := repository.FindByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("find completed job: %v", err)
		}
		expectedKey := "results/" + created.ID + ".json"
		if stored.Status != job.StatusCompleted || stored.ResultKey == nil || *stored.ResultKey != expectedKey || stored.StartedAt == nil || stored.CompletedAt == nil {
			t.Fatalf("unexpected completed job: %+v", stored)
		}

		resultContent, err := os.ReadFile(filepath.Join(storageRoot, filepath.FromSlash(expectedKey)))
		if err != nil {
			t.Fatalf("read result: %v", err)
		}
		var result loganalysis.Result
		if err := json.Unmarshal(resultContent, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.TotalRequests != 2 || result.ErrorRequests != 1 || result.ErrorRate != 0.5 || result.StatusCodes[200] != 1 || result.StatusCodes[500] != 1 {
			t.Fatalf("unexpected analysis result: %+v", result)
		}

		if err := run(ctx, processor, created.ID); !errors.Is(err, job.ErrInvalidTransition) {
			t.Fatalf("expected duplicate processing conflict, got %v", err)
		}
		storedAgain, err := repository.FindByID(ctx, created.ID)
		if err != nil || storedAgain.Status != job.StatusCompleted {
			t.Fatalf("completed job changed after duplicate run: %+v, %v", storedAgain, err)
		}
	})

	t.Run("marks invalid log failed", func(t *testing.T) {
		fileKey := "uploads/invalid.log"
		created, err := repository.Create(ctx, job.CreateParams{FileName: "invalid.log", FileKey: &fileKey})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		inputPath := filepath.Join(storageRoot, filepath.FromSlash(fileKey))
		if err := os.WriteFile(inputPath, []byte("not a combined log line\n"), 0o600); err != nil {
			t.Fatalf("write invalid log: %v", err)
		}

		err = run(ctx, processor, created.ID)
		if !errors.Is(err, loganalysis.ErrInvalidLogLine) {
			t.Fatalf("expected invalid log error, got %v", err)
		}
		stored, findErr := repository.FindByID(ctx, created.ID)
		if findErr != nil {
			t.Fatalf("find failed job: %v", findErr)
		}
		if stored.Status != job.StatusFailed || stored.ErrorMessage == nil || *stored.ErrorMessage == "" || stored.CompletedAt == nil || stored.ResultKey != nil {
			t.Fatalf("unexpected failed job: %+v", stored)
		}
		resultPath := filepath.Join(storageRoot, "results", created.ID+".json")
		if _, statErr := os.Stat(resultPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed job left a result file: %v", statErr)
		}
	})
}
