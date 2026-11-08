// Package s3storage implements object storage through an explicit S3 endpoint.
package s3storage

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	EndpointURL     string
	Bucket          string
	UsePathStyle    bool
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func ValidateBucket(name string) error {
	if !bucketName.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") || net.ParseIP(name) != nil {
		return errors.New("S3_BUCKET_NAME must be a valid bucket name")
	}
	return nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Region) == "" || strings.TrimSpace(c.Region) != c.Region {
		return errors.New("AWS_REGION is required without surrounding whitespace")
	}
	if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" {
		return errors.New("S3 AWS credentials are required")
	}
	u, err := url.ParseRequestURI(c.EndpointURL)
	if err != nil || strings.TrimSpace(c.EndpointURL) != c.EndpointURL || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
		return errors.New("S3_ENDPOINT_URL must be an absolute HTTP(S) origin without user information")
	}
	return ValidateBucket(c.Bucket)
}

func NewClient(ctx context.Context, c Config) (*s3.Client, error) {
	if ctx == nil {
		return nil, errors.New("configure S3: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, safeError("configure S3", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, "")))
	if err != nil {
		return nil, safeError("configure S3 client", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.EndpointURL); o.UsePathStyle = c.UsePathStyle }), nil
}
