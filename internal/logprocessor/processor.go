// Package logprocessor connects stored access logs to the log analysis core.
package logprocessor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"

	"cloudqueue/internal/job"
	"cloudqueue/internal/loganalysis"
	"cloudqueue/internal/worker"
)

var (
	// ErrInvalidInput indicates invalid Processor configuration or job input.
	ErrInvalidInput = errors.New("invalid log processor input")
	jobIDPattern    = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// Storage opens input objects and stores generated result objects.
type Storage interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, content io.Reader) error
}

// Analyzer calculates web request statistics from log input.
type Analyzer interface {
	Analyze(ctx context.Context, input io.Reader) (loganalysis.Result, error)
}

// Processor analyzes a stored log and writes its JSON result.
type Processor struct {
	storage  Storage
	analyzer Analyzer
}

var _ worker.Processor = (*Processor)(nil)

// New creates a local log Processor with storage and analysis dependencies.
func New(storage Storage, analyzer Analyzer) *Processor {
	return &Processor{storage: storage, analyzer: analyzer}
}

// Process reads a job log, analyzes it, stores JSON, and returns the result key.
func (p *Processor) Process(ctx context.Context, current job.Job) (string, error) {
	if p == nil {
		return "", fmt.Errorf("process log: processor is required: %w", ErrInvalidInput)
	}
	if isNil(p.storage) {
		return "", fmt.Errorf("process log: storage is required: %w", ErrInvalidInput)
	}
	if isNil(p.analyzer) {
		return "", fmt.Errorf("process log: analyzer is required: %w", ErrInvalidInput)
	}
	if isNil(ctx) {
		return "", fmt.Errorf("process log: context is required: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("process log: %w", err)
	}

	fileKey, err := validateJob(current)
	if err != nil {
		return "", fmt.Errorf("process log: validate job: %w", err)
	}
	resultKey := path.Join("results", current.ID+".json")

	input, err := p.storage.Open(ctx, fileKey)
	if err != nil {
		openErr := fmt.Errorf("process log: open input: %w", err)
		if !isNil(input) {
			return "", joinCloseError(openErr, input)
		}
		return "", openErr
	}
	if isNil(input) {
		return "", fmt.Errorf("process log: open input: storage returned nil reader: %w", ErrInvalidInput)
	}

	if err := ctx.Err(); err != nil {
		return "", joinCloseError(fmt.Errorf("process log: %w", err), input)
	}

	result, analyzeErr := p.analyzer.Analyze(ctx, input)
	closeErr := input.Close()
	if analyzeErr != nil || closeErr != nil {
		return "", joinProcessErrors(analyzeErr, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("process log: %w", err)
	}

	var content bytes.Buffer
	if err := json.NewEncoder(&content).Encode(result); err != nil {
		return "", fmt.Errorf("process log: encode result: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("process log: %w", err)
	}
	if err := p.storage.Put(ctx, resultKey, &content); err != nil {
		return "", fmt.Errorf("process log: store result: %w", err)
	}

	return resultKey, nil
}

// validateJob checks the identifiers used to read input and create the result key.
func validateJob(current job.Job) (string, error) {
	if strings.TrimSpace(current.ID) == "" || !jobIDPattern.MatchString(current.ID) {
		return "", fmt.Errorf("job id is required and must be path-safe: %w", ErrInvalidInput)
	}
	if current.FileKey == nil || strings.TrimSpace(*current.FileKey) == "" {
		return "", fmt.Errorf("file key is required: %w", ErrInvalidInput)
	}
	if err := validateStorageKey(*current.FileKey); err != nil {
		return "", err
	}
	return *current.FileKey, nil
}

// joinProcessErrors preserves analysis and input close failures together.
func joinProcessErrors(analyzeErr error, closeErr error) error {
	var wrapped []error
	if analyzeErr != nil {
		wrapped = append(wrapped, fmt.Errorf("process log: analyze input: %w", analyzeErr))
	}
	if closeErr != nil {
		wrapped = append(wrapped, fmt.Errorf("process log: close input: %w", closeErr))
	}
	return errors.Join(wrapped...)
}

// joinCloseError closes input while preserving both the original and close errors.
func joinCloseError(original error, input io.Closer) error {
	if closeErr := input.Close(); closeErr != nil {
		return errors.Join(original, fmt.Errorf("process log: close input: %w", closeErr))
	}
	return original
}

// isNil detects nil interfaces and interfaces containing nil pointers.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
