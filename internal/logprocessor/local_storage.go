package logprocessor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const localCopyBufferSize = 32 * 1024

// LocalStorage stores input and result objects below one local root directory.
type LocalStorage struct {
	root string
}

// NewLocalStorage creates local file storage rooted at the provided directory.
func NewLocalStorage(root string) *LocalStorage {
	return &LocalStorage{root: root}
}

// Open opens a validated local object for reading.
func (s *LocalStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil, fmt.Errorf("local storage: root is required: %w", ErrInvalidInput)
	}
	if isNil(ctx) {
		return nil, fmt.Errorf("local storage: context is required: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local storage: open input: %w", err)
	}

	filePath, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local storage: open input: %w", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, newStorageError("open input", key, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, joinStorageCloseError("inspect input", key, err, file)
	}
	if info.IsDir() {
		return nil, joinStorageCloseError("open input", key, ErrInvalidInput, file)
	}
	return file, nil
}

// Put atomically writes a local object through a temporary file in its destination directory.
func (s *LocalStorage) Put(ctx context.Context, key string, content io.Reader) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return fmt.Errorf("local storage: root is required: %w", ErrInvalidInput)
	}
	if isNil(ctx) {
		return fmt.Errorf("local storage: context is required: %w", ErrInvalidInput)
	}
	if isNil(content) {
		return fmt.Errorf("local storage: content is required: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local storage: store result: %w", err)
	}

	filePath, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local storage: store result: %w", err)
	}
	directory := filepath.Dir(filePath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return newStorageError("create result directory", key, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local storage: store result: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".cloudqueue-*")
	if err != nil {
		return newStorageError("create temporary file", key, err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := copyWithContext(ctx, temporary, content); err != nil {
		return joinTemporaryCloseError("write temporary file", key, err, temporary)
	}
	if err := ctx.Err(); err != nil {
		return joinTemporaryCloseError("write temporary file", key, err, temporary)
	}
	if err := temporary.Sync(); err != nil {
		return joinTemporaryCloseError("sync temporary file", key, err, temporary)
	}
	if err := temporary.Close(); err != nil {
		return newStorageError("close temporary file", key, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local storage: store result: %w", err)
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return newStorageError("rename result", key, err)
	}
	committed = true
	return nil
}

// resolve converts a portable storage key into a path confined below the root.
func (s *LocalStorage) resolve(key string) (string, error) {
	if err := validateStorageKey(key); err != nil {
		return "", fmt.Errorf("local storage: validate key: %w", err)
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", newStorageError("resolve root", key, err)
	}
	target := filepath.Join(root, filepath.FromSlash(key))
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", newStorageError("resolve key", key, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local storage: validate key: %w", ErrInvalidInput)
	}
	return target, nil
}

// validateStorageKey rejects absolute, platform-specific, and escaping object keys.
func validateStorageKey(key string) error {
	if strings.TrimSpace(key) == "" || strings.ContainsRune(key, '\x00') {
		return fmt.Errorf("storage key is required: %w", ErrInvalidInput)
	}
	if strings.Contains(key, `\`) || path.IsAbs(key) || filepath.IsAbs(key) || filepath.VolumeName(key) != "" {
		return fmt.Errorf("storage key must be relative: %w", ErrInvalidInput)
	}
	cleaned := path.Clean(key)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("storage key escapes root: %w", ErrInvalidInput)
	}
	return nil
}

// copyWithContext copies content while checking cancellation between reads and writes.
func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, localCopyBufferSize)
	reader := bufio.NewReaderSize(source, localCopyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// storageError hides absolute paths while preserving the original filesystem error.
type storageError struct {
	operation string
	key       string
	cause     error
}

// Error returns a safe storage operation description without an absolute path.
func (e *storageError) Error() string {
	return fmt.Sprintf("local storage: %s %q", e.operation, e.key)
}

// Unwrap exposes the original filesystem error to errors.Is and errors.As.
func (e *storageError) Unwrap() error {
	return e.cause
}

// newStorageError creates a sanitized filesystem operation error.
func newStorageError(operation string, key string, cause error) error {
	return &storageError{operation: operation, key: key, cause: cause}
}

// joinStorageCloseError preserves a file operation and any close failure.
func joinStorageCloseError(operation string, key string, original error, file io.Closer) error {
	operationErr := newStorageError(operation, key, original)
	if closeErr := file.Close(); closeErr != nil {
		return errors.Join(operationErr, newStorageError("close input", key, closeErr))
	}
	return operationErr
}

// joinTemporaryCloseError preserves a temporary file operation and close failure.
func joinTemporaryCloseError(operation string, key string, original error, file io.Closer) error {
	operationErr := newStorageError(operation, key, original)
	if closeErr := file.Close(); closeErr != nil {
		return errors.Join(operationErr, newStorageError("close temporary file", key, closeErr))
	}
	return operationErr
}
