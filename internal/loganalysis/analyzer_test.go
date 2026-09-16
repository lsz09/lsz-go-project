package loganalysis

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	successLine = `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /api/v1/jobs?limit=10 HTTP/1.1" 200 512 "-" "Mozilla/5.0"`
	errorLine   = `127.0.0.1 - - [16/Sep/2026:10:01:00 +0900] "POST /api/v1/jobs HTTP/2.0" 500 128 "-" "Mozilla/5.0"`
)

// TestAnalyzeCalculatesStatistics verifies all aggregates and query normalization.
func TestAnalyzeCalculatesStatistics(t *testing.T) {
	input := strings.Join([]string{
		successLine,
		`127.0.0.1 - - [16/Sep/2026:10:00:30 +0900] "GET /api/v1/jobs?offset=20 HTTP/1.1" 404 64 "-" "Mozilla/5.0"`,
		errorLine,
		`127.0.0.1 - - [16/Sep/2026:10:02:00 +0900] "DELETE /api/v1/jobs/1 HTTP/1.0" 399 - "https://example.com/\"jobs\"" "curl/8.0 \"test\""`,
	}, "\n")
	want := Result{
		TotalRequests: 4,
		ErrorRequests: 2,
		ErrorRate:     0.5,
		StatusCodes:   map[int]int{200: 1, 399: 1, 404: 1, 500: 1},
		Methods:       map[string]int{"GET": 2, "POST": 1, "DELETE": 1},
		Endpoints:     map[string]int{"/api/v1/jobs": 3, "/api/v1/jobs/1": 1},
	}

	got, err := New().Analyze(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("analyze logs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

// TestAnalyzeReturnsInitializedEmptyResult verifies empty and whitespace-only input.
func TestAnalyzeReturnsInitializedEmptyResult(t *testing.T) {
	for _, input := range []string{"", " \n\t\r\n"} {
		result, err := New().Analyze(context.Background(), strings.NewReader(input))
		if err != nil {
			t.Fatalf("analyze empty input: %v", err)
		}
		if result.TotalRequests != 0 || result.ErrorRequests != 0 || result.ErrorRate != 0 {
			t.Fatalf("expected zero counters, got %+v", result)
		}
		if result.StatusCodes == nil || result.Methods == nil || result.Endpoints == nil {
			t.Fatalf("expected initialized maps, got %+v", result)
		}
	}
}

// TestAnalyzeRejectsInvalidLogLines verifies validation errors and line numbers.
func TestAnalyzeRejectsInvalidLogLines(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "invalid format", line: "not a combined log"},
		{name: "missing method", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] " /health HTTP/1.1" 200 1 "-" "agent"`},
		{name: "lowercase method", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "get /health HTTP/1.1" 200 1 "-" "agent"`},
		{name: "missing endpoint", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET  HTTP/1.1" 200 1 "-" "agent"`},
		{name: "invalid endpoint", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET relative HTTP/1.1" 200 1 "-" "agent"`},
		{name: "missing protocol", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health" 200 1 "-" "agent"`},
		{name: "non-numeric status", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" abc 1 "-" "agent"`},
		{name: "status below range", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" 099 1 "-" "agent"`},
		{name: "status above range", line: `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" 600 1 "-" "agent"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := successLine + "\n" + test.line + "\n" + errorLine
			result, err := New().Analyze(context.Background(), strings.NewReader(input))
			if !errors.Is(err, ErrInvalidLogLine) {
				t.Fatalf("expected ErrInvalidLogLine, got %v", err)
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("expected line number in error, got %v", err)
			}
			if result.TotalRequests != 1 {
				t.Fatalf("expected analysis to stop after first request, got %+v", result)
			}
		})
	}
}

// TestAnalyzeRejectsOversizedLine verifies bounded line reading without a panic.
func TestAnalyzeRejectsOversizedLine(t *testing.T) {
	input := strings.Repeat("x", maxLineSize+1)
	_, err := New().Analyze(context.Background(), strings.NewReader(input))
	if !errors.Is(err, ErrInvalidLogLine) || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("expected oversized line error, got %v", err)
	}
}

// TestAnalyzeAcceptsLongValidLine verifies input near the configured line limit.
func TestAnalyzeAcceptsLongValidLine(t *testing.T) {
	userAgent := strings.Repeat("x", maxLineSize-len(successLine)-1)
	line := `127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" 200 1 "-" "` + userAgent + `"`
	result, err := New().Analyze(context.Background(), strings.NewReader(line))
	if err != nil {
		t.Fatalf("analyze long valid line: %v", err)
	}
	if result.TotalRequests != 1 {
		t.Fatalf("expected one request, got %+v", result)
	}
}

// TestAnalyzePreservesReaderError verifies failures returned while reading input.
func TestAnalyzePreservesReaderError(t *testing.T) {
	readErr := errors.New("read failed")
	reader := errorReader{err: readErr}

	_, err := New().Analyze(context.Background(), reader)
	if !errors.Is(err, readErr) || !strings.Contains(err.Error(), "read input") {
		t.Fatalf("expected wrapped reader error, got %v", err)
	}
}

// TestAnalyzeHonorsContext verifies cancellation before and during analysis.
func TestAnalyzeHonorsContext(t *testing.T) {
	t.Run("before analysis", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := New().Analyze(ctx, strings.NewReader(successLine))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	})

	t.Run("during analysis", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancelingReader{
			reader:      strings.NewReader(successLine + "\n" + errorLine),
			cancel:      cancel,
			chunkSize:   len(successLine) + 1,
			cancelAfter: 2,
		}

		result, err := New().Analyze(ctx, reader)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
		if result.TotalRequests != 1 {
			t.Fatalf("expected analysis to stop after one request, got %+v", result)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		_, err := New().Analyze(ctx, strings.NewReader(successLine))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
	})
}

// TestAnalyzeRejectsMissingDependencies verifies defensive nil handling.
func TestAnalyzeRejectsMissingDependencies(t *testing.T) {
	var nilAnalyzer *Analyzer
	var nilContext context.Context
	var nilReader io.Reader
	var typedNilReader *strings.Reader

	tests := []struct {
		name     string
		analyzer *Analyzer
		ctx      context.Context
		reader   io.Reader
	}{
		{name: "nil analyzer", analyzer: nilAnalyzer, ctx: context.Background(), reader: strings.NewReader("")},
		{name: "nil context", analyzer: New(), ctx: nilContext, reader: strings.NewReader("")},
		{name: "nil reader", analyzer: New(), ctx: context.Background(), reader: nilReader},
		{name: "typed nil reader", analyzer: New(), ctx: context.Background(), reader: typedNilReader},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.analyzer.Analyze(test.ctx, test.reader)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

// errorReader always returns its configured read failure.
type errorReader struct {
	err error
}

// Read returns the configured error without producing input.
func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

// cancelingReader cancels a context after its first successful read.
type cancelingReader struct {
	reader      io.Reader
	cancel      context.CancelFunc
	chunkSize   int
	cancelAfter int
	reads       int
}

// Read limits each chunk and cancels the context on the configured read.
func (r *cancelingReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.chunkSize {
		buffer = buffer[:r.chunkSize]
	}
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.reads++
	}
	if r.reads == r.cancelAfter {
		r.cancel()
	}
	return count, err
}
