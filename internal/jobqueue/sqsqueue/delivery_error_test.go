package sqsqueue

import (
	"errors"
	"strings"
	"testing"

	"cloudqueue/internal/jobqueue"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestInvalidDeliveryClassification(t *testing.T) {
	for _, message := range []types.Message{
		{},
		{Body: aws.String("{malformed-body-secret"), ReceiptHandle: aws.String("receipt-secret")},
		{Body: aws.String(`{"job_id":"invalid-secret"}`), ReceiptHandle: aws.String("receipt-secret")},
		{Body: aws.String(`{"job_id":"00000000-0000-0000-0000-000000000001","unknown-secret":true}`), ReceiptHandle: aws.String("receipt-secret")},
		{Body: aws.String(`{"job_id":"00000000-0000-0000-0000-000000000001"}`)},
	} {
		_, err := decodeDelivery(message)
		if !errors.Is(err, jobqueue.ErrInvalidDelivery) {
			t.Fatalf("invalid delivery not classified: %v", err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("body or receipt exposed")
		}
	}
}
