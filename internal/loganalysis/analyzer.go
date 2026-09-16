// Package loganalysis analyzes web server access logs without external infrastructure.
package loganalysis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const (
	readerBufferSize = 64 * 1024
	maxLineSize      = 1024 * 1024
)

var (
	// ErrInvalidInput indicates that an Analyzer dependency is missing.
	ErrInvalidInput = errors.New("invalid log analysis input")
	// ErrInvalidLogLine indicates that an access log line cannot be analyzed safely.
	ErrInvalidLogLine = errors.New("invalid log line")

	combinedLogPattern = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+\[[^\]]+\]\s+"(\S+)\s+(\S+)\s+(HTTP/[0-9]+\.[0-9]+)"\s+([0-9]{3})\s+(\S+)\s+"(\\.|[^"])*"\s+"(\\.|[^"])*"$`)
	methodPattern      = regexp.MustCompile(`^[A-Z]+$`)
)

// Result contains request statistics calculated from an access log.
type Result struct {
	TotalRequests int            `json:"total_requests"`
	ErrorRequests int            `json:"error_requests"`
	ErrorRate     float64        `json:"error_rate"`
	StatusCodes   map[int]int    `json:"status_codes"`
	Methods       map[string]int `json:"methods"`
	Endpoints     map[string]int `json:"endpoints"`
}

// Analyzer calculates request statistics from Combined Log Format input.
type Analyzer struct{}

// New creates a web server log Analyzer.
func New() *Analyzer {
	return &Analyzer{}
}

// Analyze reads access logs line by line and returns their aggregated statistics.
func (a *Analyzer) Analyze(ctx context.Context, input io.Reader) (Result, error) {
	result := newResult()
	if a == nil {
		return result, fmt.Errorf("analyze log: analyzer is required: %w", ErrInvalidInput)
	}
	if isNil(ctx) {
		return result, fmt.Errorf("analyze log: context is required: %w", ErrInvalidInput)
	}
	if isNil(input) {
		return result, fmt.Errorf("analyze log: reader is required: %w", ErrInvalidInput)
	}

	reader := bufio.NewReaderSize(input, readerBufferSize)
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("analyze log: %w", err)
		}

		line, tooLong, err := readLine(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if tooLong {
			return result, fmt.Errorf("analyze log: line %d: line too long: %w", lineNumber, ErrInvalidLogLine)
		}
		if err != nil {
			return result, fmt.Errorf("analyze log: line %d: read input: %w", lineNumber, err)
		}
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("analyze log: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		entry, err := parseLine(line)
		if err != nil {
			return result, fmt.Errorf("analyze log: line %d: %w", lineNumber, err)
		}

		result.TotalRequests++
		result.StatusCodes[entry.statusCode]++
		result.Methods[entry.method]++
		result.Endpoints[entry.endpoint]++
		if entry.statusCode >= 400 {
			result.ErrorRequests++
		}
	}

	if result.TotalRequests > 0 {
		result.ErrorRate = float64(result.ErrorRequests) / float64(result.TotalRequests)
	}

	return result, nil
}

// logEntry contains the request fields used by the statistics calculation.
type logEntry struct {
	method     string
	endpoint   string
	statusCode int
}

// newResult initializes maps so empty input produces stable JSON objects.
func newResult() Result {
	return Result{
		StatusCodes: make(map[int]int),
		Methods:     make(map[string]int),
		Endpoints:   make(map[string]int),
	}
}

// parseLine extracts the fields required from one Combined Log Format line.
func parseLine(line string) (logEntry, error) {
	matches := combinedLogPattern.FindStringSubmatch(line)
	if matches == nil {
		return logEntry{}, fmt.Errorf("parse combined format: %w", ErrInvalidLogLine)
	}

	method := matches[1]
	if !methodPattern.MatchString(method) {
		return logEntry{}, fmt.Errorf("parse request method: %w", ErrInvalidLogLine)
	}

	requestURL, err := url.ParseRequestURI(matches[2])
	if err != nil || requestURL.Path == "" {
		return logEntry{}, fmt.Errorf("parse request endpoint: %w", ErrInvalidLogLine)
	}

	statusCode, err := strconv.Atoi(matches[4])
	if err != nil || statusCode < 100 || statusCode > 599 {
		return logEntry{}, fmt.Errorf("parse status code: %w", ErrInvalidLogLine)
	}

	return logEntry{method: method, endpoint: requestURL.Path, statusCode: statusCode}, nil
}

// readLine reads one bounded line and reports input that exceeds the configured limit.
func readLine(reader *bufio.Reader) (string, bool, error) {
	line := make([]byte, 0, readerBufferSize)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxLineSize {
			return "", true, nil
		}
		line = append(line, fragment...)

		switch {
		case err == nil:
			return strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"), false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return string(line), false, nil
		default:
			return "", false, err
		}
	}
}

// isNil detects nil interface values and interfaces containing nil pointers.
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
