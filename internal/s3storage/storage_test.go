package s3storage

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"cloudqueue/internal/storagekey"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeClient struct {
	get    func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error)
	put    func(context.Context, *s3.PutObjectInput) (*s3.PutObjectOutput, error)
	delete func(context.Context, *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
}

func (c *fakeClient) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return c.get(ctx, in)
}
func (c *fakeClient) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return c.put(ctx, in)
}
func (c *fakeClient) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return c.delete(ctx, in)
}

type trackedBody struct {
	io.Reader
	closed     bool
	closeError error
}

func (b *trackedBody) Close() error { b.closed = true; return b.closeError }

func TestOpenTransfersBodyAndPreservesErrors(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	body := &trackedBody{Reader: strings.NewReader("input")}
	client := &fakeClient{get: func(received context.Context, in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		if received != ctx || aws.ToString(in.Bucket) != "test-bucket" || aws.ToString(in.Key) != "uploads/input.log" {
			t.Fatal("incorrect GetObject input")
		}
		return &s3.GetObjectOutput{Body: body}, nil
	}}
	store, err := New(client, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(ctx, "uploads/input.log")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "input" {
		t.Fatalf("body content %q: %v", data, err)
	}
	if body.closed {
		t.Fatal("body closed before caller finished")
	}
	if err := reader.Close(); err != nil || !body.closed {
		t.Fatal("Close not forwarded")
	}
	provider, closeCause := errors.New("credential-secret"), errors.New("close-secret")
	body.closeError = closeCause
	body.closed = false
	client.get = func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{Body: body}, provider
	}
	_, err = store.Open(ctx, "uploads/input.log")
	if !errors.Is(err, provider) || !errors.Is(err, closeCause) || strings.Contains(err.Error(), "secret") || !body.closed {
		t.Fatalf("unsafe or lost get/close causes: %v", err)
	}
	for _, output := range []*s3.GetObjectOutput{nil, {}} {
		client.get = func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) { return output, nil }
		if _, err := store.Open(ctx, "uploads/input.log"); err == nil {
			t.Fatal("nil body accepted")
		}
	}
}

type boundedInput struct {
	reader  io.Reader
	maxRead int
}

func (r *boundedInput) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	return r.reader.Read(p)
}

func TestPutSpoolsUnknownLengthWithBoundedMemoryAndCleans(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")
	content := strings.Repeat("log\n", 300000)
	input := &boundedInput{reader: strings.NewReader(content)}
	client := &fakeClient{put: func(received context.Context, in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		if received != ctx || aws.ToString(in.Bucket) != "test-bucket" || aws.ToString(in.Key) != "uploads/input.log" || aws.ToInt64(in.ContentLength) != int64(len(content)) {
			t.Fatal("incorrect PutObject input")
		}
		if _, ok := in.Body.(io.ReadSeeker); !ok {
			t.Fatal("SDK body is not seekable")
		}
		data, err := io.ReadAll(in.Body)
		if err != nil || string(data) != content {
			t.Fatal("stored content changed")
		}
		return &s3.PutObjectOutput{}, nil
	}}
	store, err := New(client, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "uploads/input.log", input); err != nil {
		t.Fatal(err)
	}
	if input.maxRead > 32*1024 {
		t.Fatalf("unbounded buffer %d", input.maxRead)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool remains: %v", err)
	}
	cause := errors.New("provider-secret")
	client.put = func(context.Context, *s3.PutObjectInput) (*s3.PutObjectOutput, error) { return nil, cause }
	if err := store.Put(ctx, "results/id.json", strings.NewReader("{}")); !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe put error: %v", err)
	}
	entries, err = os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed spool remains: %v", err)
	}
}

type failingInput struct{ cause error }

func (r failingInput) Read([]byte) (int, error) { return 0, r.cause }

func TestReaderFailureAndCancellationDoNotUpload(t *testing.T) {
	client := &fakeClient{put: func(context.Context, *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		t.Fatal("uploaded invalid input")
		return nil, nil
	}}
	store, _ := New(client, "test-bucket")
	cause := errors.New("reader-secret")
	if err := store.Put(context.Background(), "input", failingInput{cause}); !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, "input", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "input", nil); err == nil {
		t.Fatal("nil input accepted")
	}
}

func TestDeleteAndKeyValidation(t *testing.T) {
	ctx := context.Background()
	calls := 0
	cause := errors.New("delete-secret")
	client := &fakeClient{delete: func(received context.Context, in *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
		calls++
		if received != ctx || aws.ToString(in.Bucket) != "test-bucket" || aws.ToString(in.Key) != "uploads/a.log" {
			t.Fatal("incorrect delete target")
		}
		if calls == 1 {
			return &s3.DeleteObjectOutput{}, nil
		}
		return nil, cause
	}}
	store, _ := New(client, "test-bucket")
	if err := store.Delete(ctx, "uploads/a.log"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "uploads/a.log"); !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	for _, key := range []string{"", " ", "..", "../outside", "a/../outside", "/absolute", `C:\outside`, "a\x00b", "https://other/key"} {
		if _, err := store.Open(ctx, key); !errors.Is(err, storagekey.ErrInvalidKey) {
			t.Fatalf("unsafe Open %q: %v", key, err)
		}
		if err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, storagekey.ErrInvalidKey) {
			t.Fatalf("unsafe Put %q: %v", key, err)
		}
		if err := store.Delete(ctx, key); !errors.Is(err, storagekey.ErrInvalidKey) {
			t.Fatalf("unsafe Delete %q: %v", key, err)
		}
	}
	if calls != 2 {
		t.Fatal("SDK called for invalid key")
	}
	if _, err := store.Open(nil, "input"); err == nil {
		t.Fatal("nil context accepted")
	}
	var missing *Storage
	if err := missing.Delete(ctx, "input"); err == nil {
		t.Fatal("nil storage accepted")
	}
}

func TestOpenClosesBodyWhenRequestCanceledAfterResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &trackedBody{Reader: strings.NewReader("data")}
	store, _ := New(&fakeClient{get: func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		cancel()
		return &s3.GetObjectOutput{Body: body}, nil
	}}, "test-bucket")
	if _, err := store.Open(ctx, "input"); !errors.Is(err, context.Canceled) || !body.closed {
		t.Fatalf("canceled response body not closed: %v", err)
	}
}
