package storage

import (
	"context"
	"strings"
	"testing"

	"cloudqueue/internal/logprocessor"
	"cloudqueue/internal/s3storage"
)

func TestBackendConfigurationAndFactory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
		remove  []string
		invalid bool
		backend string
	}{
		{"default local", nil, nil, false, "local"},
		{"explicit local", map[string]string{"STORAGE_BACKEND": "local"}, nil, false, "local"},
		{"s3 without local root", map[string]string{"STORAGE_BACKEND": "s3"}, []string{"LOCAL_STORAGE_ROOT"}, false, "s3"},
		{"s3 path style false", map[string]string{"STORAGE_BACKEND": "s3", "S3_USE_PATH_STYLE": "false"}, nil, false, "s3"},
		{"empty backend", map[string]string{"STORAGE_BACKEND": ""}, nil, true, ""},
		{"unknown backend", map[string]string{"STORAGE_BACKEND": "other"}, nil, true, ""},
		{"missing local root", nil, []string{"LOCAL_STORAGE_ROOT"}, true, ""},
		{"local ignores s3", map[string]string{"S3_ENDPOINT_URL": "secret-invalid", "S3_USE_PATH_STYLE": "bad"}, nil, false, "local"},
		{"missing endpoint", map[string]string{"STORAGE_BACKEND": "s3"}, []string{"S3_ENDPOINT_URL"}, true, ""},
		{"credentials in URL", map[string]string{"STORAGE_BACKEND": "s3", "S3_ENDPOINT_URL": "http://user:secret@localhost:4566"}, nil, true, ""},
		{"invalid URL", map[string]string{"STORAGE_BACKEND": "s3", "S3_ENDPOINT_URL": "file:///secret"}, nil, true, ""},
		{"missing bucket", map[string]string{"STORAGE_BACKEND": "s3"}, []string{"S3_BUCKET_NAME"}, true, ""},
		{"invalid bucket", map[string]string{"STORAGE_BACKEND": "s3", "S3_BUCKET_NAME": "../secret"}, nil, true, ""},
		{"invalid boolean", map[string]string{"STORAGE_BACKEND": "s3", "S3_USE_PATH_STYLE": "secret"}, nil, true, ""},
		{"empty boolean", map[string]string{"STORAGE_BACKEND": "s3", "S3_USE_PATH_STYLE": ""}, nil, true, ""},
		{"missing region", map[string]string{"STORAGE_BACKEND": "s3"}, []string{"AWS_REGION"}, true, ""},
		{"missing credential", map[string]string{"STORAGE_BACKEND": "s3"}, []string{"AWS_SECRET_ACCESS_KEY"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"LOCAL_STORAGE_ROOT": t.TempDir(), "S3_ENDPOINT_URL": "http://localhost:4566", "S3_BUCKET_NAME": "test-bucket", "AWS_REGION": "ap-northeast-2", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "secret"}
			for k, v := range tc.changes {
				values[k] = v
			}
			for _, k := range tc.remove {
				delete(values, k)
			}
			config, err := ConfigFromEnvironment(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected config result: %v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("config leaked value")
				}
				return
			}
			if config.Backend != tc.backend {
				t.Fatal("wrong backend")
			}
			store, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if tc.backend == "local" {
				if _, ok := store.(*logprocessor.LocalStorage); !ok {
					t.Fatal("wrong local implementation")
				}
			} else {
				if _, ok := store.(*s3storage.Storage); !ok {
					t.Fatal("wrong s3 implementation")
				}
			}
		})
	}
}
