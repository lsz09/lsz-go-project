#!/bin/sh
set -eu

# Validate before any mutation; unset values default, explicitly empty values fail.
queue_name=${SQS_QUEUE_NAME-cloudqueue-jobs}
dlq_name=${SQS_DLQ_NAME-cloudqueue-jobs-dlq}
max_receive_count=${SQS_MAX_RECEIVE_COUNT-5}
for name in "$queue_name" "$dlq_name"; do
    case "$name" in
        ''|*[!a-zA-Z0-9_-]*) echo 'Invalid standard SQS queue name' >&2; exit 1 ;;
    esac
    [ "${#name}" -le 80 ] || { echo 'Queue name exceeds 80 characters' >&2; exit 1; }
done
[ "$queue_name" != "$dlq_name" ] || { echo 'Source and DLQ must differ' >&2; exit 1; }
case "$max_receive_count" in
    ''|*[!0-9]*) echo 'Receive count must be a positive integer' >&2; exit 1 ;;
esac
# SQS accepts 1..1000. Reject leading zeroes to keep the policy valid JSON.
[ "${#max_receive_count}" -le 4 ] && [ "$max_receive_count" -ge 1 ] && [ "$max_receive_count" -le 1000 ] || {
    echo 'Receive count must be between 1 and 1000' >&2; exit 1;
}
case "$max_receive_count" in 0*) echo 'Receive count must not have leading zeroes' >&2; exit 1 ;; esac

ensure_queue() {
    awslocal sqs get-queue-url --queue-name "$1" --query QueueUrl --output text 2>/dev/null ||
        awslocal sqs create-queue --queue-name "$1" --query QueueUrl --output text
}

dlq_url=$(ensure_queue "$dlq_name")
awslocal sqs set-queue-attributes --queue-url "$dlq_url" --attributes MessageRetentionPeriod=1209600
dlq_arn=$(awslocal sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn --query Attributes.QueueArn --output text)
queue_url=$(ensure_queue "$queue_name")
# AWS CLI JSON attributes require RedrivePolicy to be a JSON-encoded string.
attributes=$(printf '{"VisibilityTimeout":"30","RedrivePolicy":"{\\"deadLetterTargetArn\\":\\"%s\\",\\"maxReceiveCount\\":%s}"}' "$dlq_arn" "$max_receive_count")
awslocal sqs set-queue-attributes --queue-url "$queue_url" --attributes "$attributes"
