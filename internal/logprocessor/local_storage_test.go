package logprocessor

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalStorageOpen verifies local input reads and sanitized filesystem errors.
func TestLocalStorageOpen(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "uploads", "job-id", "access.log")
	if err := os.MkdirAll(filepath.Dir(inputPath), 0o750); err != nil {
		t.Fatalf("create input directory: %v", err)
	}
	if err := os.WriteFile(inputPath, []byte("access log"), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	storage := NewLocalStorage(root)

	input, err := storage.Open(context.Background(), "uploads/job-id/access.log")
	if err != nil {
		t.Fatalf("open input: %v", err)
	}
	content, err := io.ReadAll(input)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	if string(content) != "access log" {
		t.Fatalf("expected access log, got %q", content)
	}

	_, err = storage.Open(context.Background(), "uploads/missing.log")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("absolute root leaked in error: %v", err)
	}

	_, err = storage.Open(context.Background(), "uploads/job-id")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected directory input error, got %v", err)
	}
}

// TestLocalStoragePutWritesAtomically verifies nested result creation and temp cleanup.
func TestLocalStoragePutWritesAtomically(t *testing.T) {
	root := t.TempDir()
	storage := NewLocalStorage(root)
	const key = "results/job-id.json"
	const want = `{"total_requests":1}`

	if err := storage.Put(context.Background(), key, strings.NewReader(want)); err != nil {
		t.Fatalf("put result: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "results", "job-id.json"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(content) != want {
		t.Fatalf("expected %q, got %q", want, content)
	}
	assertNoTemporaryFiles(t, filepath.Join(root, "results"))
}

// TestLocalStorageRejectsUnsafeKeys verifies portable path traversal protection.
func TestLocalStorageRejectsUnsafeKeys(t *testing.T) {
	storage := NewLocalStorage(t.TempDir())
	keys := []string{
		"",
		" ",
		".",
		"..",
		"../access.log",
		"uploads/../../access.log",
		"/var/log/access.log",
		`C:\outside\access.log`,
		`uploads\access.log`,
		"uploads/\x00/access.log",
	}

	for _, key := range keys {
		t.Run(strings.ReplaceAll(key, "/", "_"), func(t *testing.T) {
			if _, err := storage.Open(context.Background(), key); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("Open(%q): expected ErrInvalidInput, got %v", key, err)
			}
			if err := storage.Put(context.Background(), key, strings.NewReader("result")); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("Put(%q): expected ErrInvalidInput, got %v", key, err)
			}
		})
	}
}

// TestLocalStoragePutCleansUpFailures verifies no final or temporary file survives a failed write.
func TestLocalStoragePutCleansUpFailures(t *testing.T) {
	root := t.TempDir()
	storage := NewLocalStorage(root)
	writeErr := errors.New("source read failed")
	key := "results/job-id.json"

	err := storage.Put(context.Background(), key, failingReader{err: writeErr})
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected source error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "results", "job-id.json")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("expected no final result, got %v", statErr)
	}
	assertNoTemporaryFiles(t, filepath.Join(root, "results"))
}

// TestLocalStoragePutPreservesFilesystemStageErrors verifies directory and rename failures.
func TestLocalStoragePutPreservesFilesystemStageErrors(t *testing.T) {
	t.Run("create result directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "results"), []byte("blocking file"), 0o600); err != nil {
			t.Fatalf("create blocking file: %v", err)
		}
		err := NewLocalStorage(root).Put(context.Background(), "results/job-id.json", strings.NewReader("result"))
		if err == nil || !strings.Contains(err.Error(), "create result directory") {
			t.Fatalf("expected directory error, got %v", err)
		}
	})

	t.Run("rename result", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "results", "job-id.json")
		if err := os.MkdirAll(filepath.Join(target, "child"), 0o750); err != nil {
			t.Fatalf("create blocking directory: %v", err)
		}
		err := NewLocalStorage(root).Put(context.Background(), "results/job-id.json", strings.NewReader("result"))
		if err == nil || !strings.Contains(err.Error(), "rename result") {
			t.Fatalf("expected rename error, got %v", err)
		}
		assertNoTemporaryFiles(t, filepath.Join(root, "results"))
	})
}

// TestLocalStorageHonorsContext verifies cancellation before and during writes.
func TestLocalStorageHonorsContext(t *testing.T) {
	t.Run("before open", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewLocalStorage(t.TempDir()).Open(ctx, "uploads/access.log")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	})

	t.Run("during put", func(t *testing.T) {
		root := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		content := &cancelingContentReader{
			reader: strings.NewReader(strings.Repeat("x", localCopyBufferSize*2)),
			cancel: cancel,
		}
		err := NewLocalStorage(root).Put(ctx, "results/job-id.json", content)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "results", "job-id.json")); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("expected no final result, got %v", statErr)
		}
		assertNoTemporaryFiles(t, filepath.Join(root, "results"))
	})
}

// TestLocalStorageRejectsMissingDependencies verifies nil configuration and content.
func TestLocalStorageRejectsMissingDependencies(t *testing.T) {
	var nilStorage *LocalStorage
	var typedNilReader *strings.Reader

	tests := []struct {
		name    string
		storage *LocalStorage
		ctx     context.Context
		content io.Reader
	}{
		{name: "nil storage", storage: nilStorage, ctx: context.Background(), content: strings.NewReader("result")},
		{name: "empty root", storage: NewLocalStorage(""), ctx: context.Background(), content: strings.NewReader("result")},
		{name: "blank root", storage: NewLocalStorage(" "), ctx: context.Background(), content: strings.NewReader("result")},
		{name: "nil context", storage: NewLocalStorage(t.TempDir()), ctx: nil, content: strings.NewReader("result")},
		{name: "nil content", storage: NewLocalStorage(t.TempDir()), ctx: context.Background(), content: nil},
		{name: "typed nil content", storage: NewLocalStorage(t.TempDir()), ctx: context.Background(), content: typedNilReader},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.storage.Put(test.ctx, "results/job-id.json", test.content); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}

	if _, err := NewLocalStorage("").Open(context.Background(), "uploads/access.log"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid root error from Open, got %v", err)
	}
	if _, err := NewLocalStorage(t.TempDir()).Open(nil, "uploads/access.log"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid context error from Open, got %v", err)
	}
}

// TestCopyWithContextDetectsShortWrite verifies incomplete writes are rejected.
func TestCopyWithContextDetectsShortWrite(t *testing.T) {
	err := copyWithContext(context.Background(), shortWriter{}, strings.NewReader("content"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected io.ErrShortWrite, got %v", err)
	}
}

// failingReader returns its configured failure without producing data.
type failingReader struct {
	err error
}

// Read returns the configured source error.
func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

// cancelingContentReader cancels its context after producing the first chunk.
type cancelingContentReader struct {
	reader io.Reader
	cancel context.CancelFunc
	read   bool
}

// Read forwards content and cancels once data has been produced.
func (r *cancelingContentReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1024 {
		buffer = buffer[:1024]
	}
	count, err := r.reader.Read(buffer)
	if !r.read && count > 0 {
		r.read = true
		r.cancel()
	}
	return count, err
}

// shortWriter reports an incomplete write without its required error.
type shortWriter struct{}

// Write deliberately violates the Writer contract to exercise defensive handling.
func (shortWriter) Write(buffer []byte) (int, error) {
	return len(buffer) - 1, nil
}

// assertNoTemporaryFiles verifies atomic write artifacts are removed.
func assertNoTemporaryFiles(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read result directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cloudqueue-") {
			t.Fatalf("temporary file was not removed: %s", entry.Name())
		}
	}
}
