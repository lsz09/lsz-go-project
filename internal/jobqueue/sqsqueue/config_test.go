package sqsqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Region:          "ap-northeast-2",
		AccessKeyID:     "test",
		SecretAccessKey: "super-secret-test-value",
		EndpointURL:     "http://localhost:4566",
		QueueURL:        "http://localhost:4566/000000000000/cloudqueue-jobs",
		WaitTimeSeconds: 10,
	}
}

func TestConfigFromEnvironment(t *testing.T) {
	values := map[string]string{
		"AWS_REGION":            "ap-northeast-2",
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "super-secret-test-value",
		"SQS_ENDPOINT_URL":      "http://localhost:4566",
		"SQS_QUEUE_URL":         "http://localhost:4566/000000000000/cloudqueue-jobs",
		"SQS_WAIT_TIME_SECONDS": "10",
	}
	config, err := ConfigFromEnvironment(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("config from environment: %v", err)
	}
	if config != validConfig() {
		t.Fatalf("unexpected config: %+v", config)
	}

	if _, err := ConfigFromEnvironment(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("expected invalid nil lookup, got %v", err)
	}
	values["SQS_WAIT_TIME_SECONDS"] = "not-an-integer-secret"
	_, err = ConfigFromEnvironment(func(name string) string { return values[name] })
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), values["SQS_WAIT_TIME_SECONDS"]) {
		t.Fatalf("unsafe wait time error: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing region", mutate: func(c *Config) { c.Region = "" }},
		{name: "blank region", mutate: func(c *Config) { c.Region = "   " }},
		{name: "region whitespace", mutate: func(c *Config) { c.Region = " ap-northeast-2" }},
		{name: "missing access key", mutate: func(c *Config) { c.AccessKeyID = "" }},
		{name: "missing secret key", mutate: func(c *Config) { c.SecretAccessKey = "" }},
		{name: "missing endpoint", mutate: func(c *Config) { c.EndpointURL = "" }},
		{name: "invalid endpoint", mutate: func(c *Config) { c.EndpointURL = "localhost:4566" }},
		{name: "endpoint credentials", mutate: func(c *Config) { c.EndpointURL = "http://user:password@localhost:4566" }},
		{name: "missing queue URL", mutate: func(c *Config) { c.QueueURL = "" }},
		{name: "invalid queue URL", mutate: func(c *Config) { c.QueueURL = "://queue" }},
		{name: "negative wait", mutate: func(c *Config) { c.WaitTimeSeconds = -1 }},
		{name: "wait over maximum", mutate: func(c *Config) { c.WaitTimeSeconds = 21 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			err := config.Validate()
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("expected invalid configuration, got %v", err)
			}
			if strings.Contains(err.Error(), "super-secret-test-value") || strings.Contains(err.Error(), "password") {
				t.Fatalf("configuration error exposed credentials: %v", err)
			}
		})
	}

	for _, wait := range []int32{0, 1, MaxWaitTimeSeconds} {
		config := validConfig()
		config.WaitTimeSeconds = wait
		if err := config.Validate(); err != nil {
			t.Fatalf("wait time %d should be valid: %v", wait, err)
		}
	}
}

func TestNewClient(t *testing.T) {
	client, err := NewClient(context.Background(), validConfig())
	if err != nil || client == nil {
		t.Fatalf("new client: %v", err)
	}
	if endpoint := client.Options().BaseEndpoint; endpoint == nil || *endpoint != validConfig().EndpointURL {
		t.Fatalf("unexpected base endpoint: %v", endpoint)
	}

	if _, err := NewClient(nil, validConfig()); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("expected nil context error, got %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(canceled, validConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := NewClient(expired, validConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
}
