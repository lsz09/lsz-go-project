// Package sqsqueue implements the job queue contracts with AWS SQS.
package sqsqueue

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const MaxWaitTimeSeconds int32 = 20

var ErrInvalidConfiguration = errors.New("invalid SQS configuration")

// Config contains the explicit SQS settings used by LocalStack or AWS.
type Config struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	EndpointURL     string
	QueueURL        string
	WaitTimeSeconds int32
}

// ConfigFromEnvironment reads and validates SQS settings without logging their values.
func ConfigFromEnvironment(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, configurationError("environment lookup is required", nil)
	}

	waitTime, err := strconv.ParseInt(getenv("SQS_WAIT_TIME_SECONDS"), 10, 32)
	if err != nil {
		return Config{}, configurationError("SQS_WAIT_TIME_SECONDS must be an integer", err)
	}
	config := Config{
		Region:          getenv("AWS_REGION"),
		AccessKeyID:     getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: getenv("AWS_SECRET_ACCESS_KEY"),
		EndpointURL:     getenv("SQS_ENDPOINT_URL"),
		QueueURL:        getenv("SQS_QUEUE_URL"),
		WaitTimeSeconds: int32(waitTime),
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate rejects incomplete, unsafe, or unsupported SQS settings.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Region) == "" || strings.TrimSpace(c.Region) != c.Region {
		return configurationError("AWS_REGION is required without surrounding whitespace", nil)
	}
	if strings.TrimSpace(c.AccessKeyID) == "" {
		return configurationError("AWS_ACCESS_KEY_ID is required", nil)
	}
	if strings.TrimSpace(c.SecretAccessKey) == "" {
		return configurationError("AWS_SECRET_ACCESS_KEY is required", nil)
	}
	if err := validateHTTPURL(c.EndpointURL); err != nil {
		return configurationError("SQS_ENDPOINT_URL must be an absolute HTTP(S) URL", err)
	}
	if err := validateHTTPURL(c.QueueURL); err != nil {
		return configurationError("SQS_QUEUE_URL must be an absolute HTTP(S) URL", err)
	}
	if c.WaitTimeSeconds < 0 || c.WaitTimeSeconds > MaxWaitTimeSeconds {
		return configurationError("SQS_WAIT_TIME_SECONDS must be between 0 and 20", nil)
	}
	return nil
}

// NewClient creates an AWS SDK client configured for an explicit SQS endpoint.
func NewClient(ctx context.Context, config Config) (*sqs.Client, error) {
	if ctx == nil {
		return nil, errors.New("create SQS client: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, operationError("create SQS client", err)
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("create SQS client: %w", err)
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(config.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.AccessKeyID,
			config.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, operationError("create SQS client: load AWS configuration", err)
	}

	client := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) {
		options.BaseEndpoint = aws.String(config.EndpointURL)
	})
	return client, nil
}

func validateHTTPURL(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return errors.New("URL is required without surrounding whitespace")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("URL must use HTTP(S), include a host, and exclude user information")
	}
	return nil
}

func configurationError(detail string, cause error) error {
	errorsToJoin := []error{ErrInvalidConfiguration}
	if cause != nil {
		errorsToJoin = append(errorsToJoin, &safeCause{description: detail, cause: cause})
	}
	return fmt.Errorf("validate SQS configuration: %s: %w", detail, errors.Join(errorsToJoin...))
}
