package logprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloudqueue/internal/job"
	"cloudqueue/internal/loganalysis"
)

// stubStorage replaces object storage in Processor unit tests.
type stubStorage struct {
	open func(context.Context, string) (io.ReadCloser, error)
	put  func(context.Context, string, io.Reader) error
}

// Open runs the input behavior configured by the current test.
func (s stubStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.open(ctx, key)
}

// Put runs the result behavior configured by the current test.
func (s stubStorage) Put(ctx context.Context, key string, content io.Reader) error {
	return s.put(ctx, key, content)
}

// stubAnalyzer replaces log analysis in Processor unit tests.
type stubAnalyzer struct {
	analyze func(context.Context, io.Reader) (loganalysis.Result, error)
}

// Analyze runs the analysis behavior configured by the current test.
func (s stubAnalyzer) Analyze(ctx context.Context, input io.Reader) (loganalysis.Result, error) {
	return s.analyze(ctx, input)
}

// trackingReadCloser records close calls and can return a configured close error.
type trackingReadCloser struct {
	reader   io.Reader
	closeErr error
	closed   bool
}

// Read forwards bytes from the configured reader.
func (r *trackingReadCloser) Read(buffer []byte) (int, error) {
	return r.reader.Read(buffer)
}

// Close records the call and returns the configured error.
func (r *trackingReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

// TestProcessorProcessesAndStoresResult verifies the complete successful flow.
func TestProcessorProcessesAndStoresResult(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	const fileKey = "uploads/00000000-0000-0000-0000-000000000001/access.log"
	const resultKey = "results/00000000-0000-0000-0000-000000000001.json"
	type contextKey string
	const requestIDKey contextKey = "request-id"
	ctx := context.WithValue(context.Background(), requestIDKey, "request-1")
	input := &trackingReadCloser{reader: strings.NewReader("access log")}
	wantResult := loganalysis.Result{
		TotalRequests: 2,
		ErrorRequests: 1,
		ErrorRate:     0.5,
		StatusCodes:   map[int]int{200: 1, 500: 1},
		Methods:       map[string]int{"GET": 2},
		Endpoints:     map[string]int{"/health": 2},
	}

	storage := stubStorage{
		open: func(gotContext context.Context, gotKey string) (io.ReadCloser, error) {
			if gotContext != ctx || gotKey != fileKey {
				t.Fatalf("unexpected Open input: %v %q", gotContext, gotKey)
			}
			return input, nil
		},
		put: func(gotContext context.Context, gotKey string, content io.Reader) error {
			if gotContext != ctx || gotKey != resultKey {
				t.Fatalf("unexpected Put input: %v %q", gotContext, gotKey)
			}
			var gotResult loganalysis.Result
			if err := json.NewDecoder(content).Decode(&gotResult); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if !reflect.DeepEqual(gotResult, wantResult) {
				t.Fatalf("expected %+v, got %+v", wantResult, gotResult)
			}
			return nil
		},
	}
	analyzer := stubAnalyzer{analyze: func(gotContext context.Context, gotInput io.Reader) (loganalysis.Result, error) {
		if gotContext != ctx || gotInput != input {
			t.Fatal("caller context or input was not propagated to Analyzer")
		}
		return wantResult, nil
	}}

	gotKey, err := New(storage, analyzer).Process(ctx, job.Job{
		ID:      jobID,
		Status:  job.StatusProcessing,
		FileKey: stringPointer(fileKey),
	})
	if err != nil {
		t.Fatalf("process log: %v", err)
	}
	if gotKey != resultKey {
		t.Fatalf("expected result key %q, got %q", resultKey, gotKey)
	}
	if !input.closed {
		t.Fatal("expected input to be closed")
	}
}

// TestProcessorWithLocalStorageAndAnalyzer verifies the complete local processing integration.
func TestProcessorWithLocalStorageAndAnalyzer(t *testing.T) {
	const jobID = "00000000-0000-0000-0000-000000000001"
	const fileKey = "uploads/00000000-0000-0000-0000-000000000001/access.log"
	root := t.TempDir()
	inputPath := filepath.Join(root, filepath.FromSlash(fileKey))
	if err := os.MkdirAll(filepath.Dir(inputPath), 0o750); err != nil {
		t.Fatalf("create input directory: %v", err)
	}
	logs := strings.Join([]string{
		`127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" 200 42 "-" "Mozilla/5.0"`,
		`127.0.0.1 - - [16/Sep/2026:10:01:00 +0900] "POST /api/v1/jobs HTTP/1.1" 500 128 "-" "Mozilla/5.0"`,
	}, "\n")
	if err := os.WriteFile(inputPath, []byte(logs), 0o600); err != nil {
		t.Fatalf("write input log: %v", err)
	}

	processor := New(NewLocalStorage(root), loganalysis.New())
	resultKey, err := processor.Process(context.Background(), job.Job{
		ID:      jobID,
		Status:  job.StatusProcessing,
		FileKey: stringPointer(fileKey),
	})
	if err != nil {
		t.Fatalf("process local log: %v", err)
	}
	if resultKey != "results/"+jobID+".json" {
		t.Fatalf("unexpected result key %q", resultKey)
	}

	resultFile, err := os.Open(filepath.Join(root, filepath.FromSlash(resultKey)))
	if err != nil {
		t.Fatalf("open result: %v", err)
	}
	defer resultFile.Close()
	var result loganalysis.Result
	if err := json.NewDecoder(resultFile).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.TotalRequests != 2 || result.ErrorRequests != 1 || result.ErrorRate != 0.5 {
		t.Fatalf("unexpected analysis result %+v", result)
	}
}

// TestProcessorRejectsInvalidJob verifies job identifiers before storage access.
func TestProcessorRejectsInvalidJob(t *testing.T) {
	validKey := "uploads/job-id/access.log"
	tests := []struct {
		name string
		job  job.Job
	}{
		{name: "empty id", job: job.Job{FileKey: &validKey}},
		{name: "blank id", job: job.Job{ID: " ", FileKey: &validKey}},
		{name: "unsafe id", job: job.Job{ID: "../job", FileKey: &validKey}},
		{name: "nil file key", job: job.Job{ID: "job-id"}},
		{name: "empty file key", job: job.Job{ID: "job-id", FileKey: stringPointer("")}},
		{name: "blank file key", job: job.Job{ID: "job-id", FileKey: stringPointer(" ")}},
		{name: "escaping file key", job: job.Job{ID: "job-id", FileKey: stringPointer("../access.log")}},
		{name: "absolute file key", job: job.Job{ID: "job-id", FileKey: stringPointer("/var/log/access.log")}},
		{name: "windows file key", job: job.Job{ID: "job-id", FileKey: stringPointer(`C:\logs\access.log`)}},
	}

	storage := stubStorage{
		open: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("Storage.Open must not be called")
			return nil, nil
		},
	}
	analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		t.Fatal("Analyzer must not be called")
		return loganalysis.Result{}, nil
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(storage, analyzer).Process(context.Background(), test.job)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

// TestProcessorPreservesOpenAndStoreErrors verifies storage failures remain inspectable.
func TestProcessorPreservesOpenAndStoreErrors(t *testing.T) {
	current := validJob()
	analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		return loganalysis.Result{StatusCodes: map[int]int{}, Methods: map[string]int{}, Endpoints: map[string]int{}}, nil
	}}

	t.Run("open", func(t *testing.T) {
		storage := stubStorage{open: func(context.Context, string) (io.ReadCloser, error) {
			return nil, fs.ErrNotExist
		}}
		_, err := New(storage, analyzer).Process(context.Background(), current)
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "open input") {
			t.Fatalf("expected wrapped open error, got %v", err)
		}
	})

	t.Run("open and close", func(t *testing.T) {
		openErr := errors.New("open failed")
		closeErr := errors.New("close failed")
		input := &trackingReadCloser{reader: strings.NewReader(""), closeErr: closeErr}
		storage := stubStorage{open: func(context.Context, string) (io.ReadCloser, error) {
			return input, openErr
		}}
		_, err := New(storage, analyzer).Process(context.Background(), current)
		if !errors.Is(err, openErr) || !errors.Is(err, closeErr) || !input.closed {
			t.Fatalf("expected open and close errors, got %v closed=%v", err, input.closed)
		}
	})

	t.Run("store", func(t *testing.T) {
		storeErr := errors.New("store failed")
		storage := stubStorage{
			open: func(context.Context, string) (io.ReadCloser, error) {
				return &trackingReadCloser{reader: strings.NewReader("log")}, nil
			},
			put: func(context.Context, string, io.Reader) error { return storeErr },
		}
		_, err := New(storage, analyzer).Process(context.Background(), current)
		if !errors.Is(err, storeErr) || !strings.Contains(err.Error(), "store result") {
			t.Fatalf("expected wrapped store error, got %v", err)
		}
	})
}

// TestProcessorPreservesAnalyzeAndCloseErrors verifies both failures survive together.
func TestProcessorPreservesAnalyzeAndCloseErrors(t *testing.T) {
	analyzeErr := errors.New("analysis failed")
	closeErr := errors.New("close failed")
	input := &trackingReadCloser{reader: strings.NewReader("log"), closeErr: closeErr}
	storage := stubStorage{
		open: func(context.Context, string) (io.ReadCloser, error) { return input, nil },
		put: func(context.Context, string, io.Reader) error {
			t.Fatal("Storage.Put must not be called")
			return nil
		},
	}
	analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		return loganalysis.Result{}, analyzeErr
	}}

	_, err := New(storage, analyzer).Process(context.Background(), validJob())
	if !errors.Is(err, analyzeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected analysis and close errors, got %v", err)
	}
}

// TestProcessorPreservesCloseErrorAfterSuccessfulAnalysis verifies close failures stop storage.
func TestProcessorPreservesCloseErrorAfterSuccessfulAnalysis(t *testing.T) {
	closeErr := errors.New("close failed")
	storage := stubStorage{
		open: func(context.Context, string) (io.ReadCloser, error) {
			return &trackingReadCloser{reader: strings.NewReader("log"), closeErr: closeErr}, nil
		},
		put: func(context.Context, string, io.Reader) error {
			t.Fatal("Storage.Put must not be called")
			return nil
		},
	}
	analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		return loganalysis.Result{}, nil
	}}

	_, err := New(storage, analyzer).Process(context.Background(), validJob())
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

// TestProcessorStopsAtContextBoundaries verifies cancellation prevents later stages.
func TestProcessorStopsAtContextBoundaries(t *testing.T) {
	t.Run("before processing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := New(stubStorage{}, stubAnalyzer{}).Process(ctx, validJob())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	})

	t.Run("after open", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		input := &trackingReadCloser{reader: strings.NewReader("log")}
		storage := stubStorage{open: func(context.Context, string) (io.ReadCloser, error) {
			cancel()
			return input, nil
		}}
		analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
			t.Fatal("Analyzer must not be called")
			return loganalysis.Result{}, nil
		}}
		_, err := New(storage, analyzer).Process(ctx, validJob())
		if !errors.Is(err, context.Canceled) || !input.closed {
			t.Fatalf("expected cancellation and closed input, got %v closed=%v", err, input.closed)
		}
	})

	t.Run("after analysis", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		storage := stubStorage{
			open: func(context.Context, string) (io.ReadCloser, error) {
				return &trackingReadCloser{reader: strings.NewReader("log")}, nil
			},
			put: func(context.Context, string, io.Reader) error {
				t.Fatal("Storage.Put must not be called")
				return nil
			},
		}
		analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
			cancel()
			return loganalysis.Result{}, nil
		}}
		_, err := New(storage, analyzer).Process(ctx, validJob())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := New(stubStorage{}, stubAnalyzer{}).Process(ctx, validJob())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
	})
}

// TestProcessorRejectsMissingDependencies verifies nil values never panic.
func TestProcessorRejectsMissingDependencies(t *testing.T) {
	validStorage := stubStorage{open: func(context.Context, string) (io.ReadCloser, error) {
		return &trackingReadCloser{reader: strings.NewReader("log")}, nil
	}}
	validAnalyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		return loganalysis.Result{}, nil
	}}
	var nilProcessor *Processor
	var typedNilStorage *LocalStorage

	tests := []struct {
		name      string
		processor *Processor
		ctx       context.Context
	}{
		{name: "nil processor", processor: nilProcessor, ctx: context.Background()},
		{name: "nil storage", processor: New(nil, validAnalyzer), ctx: context.Background()},
		{name: "typed nil storage", processor: New(typedNilStorage, validAnalyzer), ctx: context.Background()},
		{name: "nil analyzer", processor: New(validStorage, nil), ctx: context.Background()},
		{name: "nil context", processor: New(validStorage, validAnalyzer), ctx: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.processor.Process(test.ctx, validJob())
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

// TestProcessorRejectsNilReaderFromStorage verifies a broken Storage cannot cause a panic.
func TestProcessorRejectsNilReaderFromStorage(t *testing.T) {
	storage := stubStorage{open: func(context.Context, string) (io.ReadCloser, error) { return nil, nil }}
	analyzer := stubAnalyzer{analyze: func(context.Context, io.Reader) (loganalysis.Result, error) {
		t.Fatal("Analyzer must not be called")
		return loganalysis.Result{}, nil
	}}
	_, err := New(storage, analyzer).Process(context.Background(), validJob())
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// validJob returns a path-safe processing job for unit tests.
func validJob() job.Job {
	return job.Job{
		ID:      "00000000-0000-0000-0000-000000000001",
		Status:  job.StatusProcessing,
		FileKey: stringPointer("uploads/00000000-0000-0000-0000-000000000001/access.log"),
	}
}

// stringPointer returns a pointer to the supplied string.
func stringPointer(value string) *string {
	return &value
}
