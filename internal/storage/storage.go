// Package storage assembles the same storage backend for the API and Worker.
package storage

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"cloudqueue/internal/logprocessor"
	"cloudqueue/internal/s3storage"
)

type Store interface {
	Open(context.Context, string) (io.ReadCloser, error)
	Put(context.Context, string, io.Reader) error
	Delete(context.Context, string) error
}

type Config struct {
	Backend   string
	LocalRoot string
	S3        s3storage.Config
}

func (c Config) Validate() error {
	switch c.Backend {
	case "local":
		if strings.TrimSpace(c.LocalRoot) == "" {
			return errors.New("LOCAL_STORAGE_ROOT is required")
		}
		return nil
	case "s3":
		return c.S3.Validate()
	default:
		return errors.New("STORAGE_BACKEND must be local or s3")
	}
}

func ConfigFromEnvironment(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("storage: environment lookup is required")
	}
	get := func(key string) string { v, _ := lookup(key); return v }
	backend, exists := lookup("STORAGE_BACKEND")
	if !exists {
		backend = "local"
	}
	c := Config{Backend: backend}
	if backend == "local" {
		c.LocalRoot = get("LOCAL_STORAGE_ROOT")
	} else if backend == "s3" {
		pathStyle := true
		if raw, ok := lookup("S3_USE_PATH_STYLE"); ok {
			var err error
			pathStyle, err = strconv.ParseBool(raw)
			if err != nil {
				return Config{}, errors.New("S3_USE_PATH_STYLE must be a boolean")
			}
		}
		c.S3 = s3storage.Config{Region: get("AWS_REGION"), AccessKeyID: get("AWS_ACCESS_KEY_ID"), SecretAccessKey: get("AWS_SECRET_ACCESS_KEY"), EndpointURL: get("S3_ENDPOINT_URL"), Bucket: get("S3_BUCKET_NAME"), UsePathStyle: pathStyle}
	}
	return c, c.Validate()
}

var (
	_ Store = (*logprocessor.LocalStorage)(nil)
	_ Store = (*s3storage.Storage)(nil)
)

// New configures a client only; buckets are provisioned by LocalStack initialization.
func New(ctx context.Context, c Config) (Store, error) {
	if ctx == nil {
		return nil, errors.New("storage: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Backend == "local" {
		return logprocessor.NewLocalStorage(c.LocalRoot), nil
	}
	client, err := s3storage.NewClient(ctx, c.S3)
	if err != nil {
		return nil, err
	}
	return s3storage.New(client, c.S3.Bucket)
}
