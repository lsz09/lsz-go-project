package s3storage

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"

	"cloudqueue/internal/storagekey"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type Storage struct {
	client Client
	bucket string
}

func New(client Client, bucket string) (*Storage, error) {
	if isNil(client) {
		return nil, errors.New("S3 storage: client is required")
	}
	if err := ValidateBucket(bucket); err != nil {
		return nil, err
	}
	return &Storage{client: client, bucket: bucket}, nil
}

func (s *Storage) validate(ctx context.Context, key string) error {
	if s == nil || isNil(s.client) || isNil(ctx) {
		return errors.New("S3 storage: dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return safeError("S3 storage: context", err)
	}
	return storagekey.Validate(key)
}

// Open transfers ownership of the streaming response Body to the caller.
func (s *Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := s.validate(ctx, key); err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if output != nil && !isNil(output.Body) {
			err = errors.Join(err, output.Body.Close())
		}
		return nil, safeError("S3 storage: open input", err)
	}
	if output == nil || isNil(output.Body) {
		return nil, errors.New("S3 storage: missing input body")
	}
	return &safeBody{body: output.Body}, nil
}

// Put spools arbitrary Readers to a private temporary file with a bounded buffer.
// This gives PutObject a seekable body and known length without loading the object
// into memory. The caller retains ownership of content; the spool is always removed.
func (s *Storage) Put(ctx context.Context, key string, content io.Reader) (resultErr error) {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	if isNil(content) {
		return errors.New("S3 storage: content is required")
	}
	file, err := os.CreateTemp("", "cloudqueue-s3-*")
	if err != nil {
		return safeError("S3 storage: create spool", err)
	}
	defer func() {
		closeErr := file.Close()
		removeErr := os.Remove(file.Name())
		if closeErr != nil || removeErr != nil {
			resultErr = safeError("S3 storage: clean spool", errors.Join(resultErr, closeErr, removeErr))
		}
	}()
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return safeError("S3 storage: spool input", err)
		}
		n, readErr := content.Read(buffer)
		if n > 0 {
			written, err := file.Write(buffer[:n])
			size += int64(written)
			if err != nil {
				return safeError("S3 storage: spool input", err)
			}
			if written != n {
				return safeError("S3 storage: spool input", io.ErrShortWrite)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return safeError("S3 storage: read input", readErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return safeError("S3 storage: store object", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return safeError("S3 storage: rewind spool", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: file, ContentLength: aws.Int64(size)})
	if err != nil {
		return safeError("S3 storage: store object", err)
	}
	return nil
}

func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return safeError("S3 storage: delete object", err)
	}
	return nil
}

type safeBody struct{ body io.ReadCloser }

func (b *safeBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, safeError("S3 storage: read body", err)
	}
	return n, err
}
func (b *safeBody) Close() error {
	if err := b.body.Close(); err != nil {
		return safeError("S3 storage: close body", err)
	}
	return nil
}

type operationError struct {
	operation string
	cause     error
}

func (e *operationError) Error() string { return e.operation }
func (e *operationError) Unwrap() error { return e.cause }
func safeError(operation string, cause error) error {
	return &operationError{operation: operation, cause: cause}
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(v).IsNil()
	}
	return false
}
