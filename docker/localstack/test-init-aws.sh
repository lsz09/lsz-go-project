#!/bin/sh
set -eu

# Run only inside a disposable LocalStack service. All queues have unique names.
prefix="cloudqueue-init-test-$(date +%s)-$$"
export SQS_QUEUE_NAME="$prefix"
export SQS_DLQ_NAME="$prefix-dlq"
export SQS_MAX_RECEIVE_COUNT=5
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cleanup() {
    for name in "$SQS_QUEUE_NAME" "$SQS_DLQ_NAME"; do
        url=$(awslocal sqs get-queue-url --queue-name "$name" --query QueueUrl --output text 2>/dev/null) || continue
        awslocal sqs delete-queue --queue-url "$url"
    done
}
trap cleanup EXIT
sh "$script_dir/init-aws.sh"
url=$(awslocal sqs get-queue-url --queue-name "$SQS_QUEUE_NAME" --query QueueUrl --output text)
dlq_url=$(awslocal sqs get-queue-url --queue-name "$SQS_DLQ_NAME" --query QueueUrl --output text)
dlq_arn=$(awslocal sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn --query Attributes.QueueArn --output text)
export EXPECTED_DLQ_ARN="$dlq_arn"
awslocal sqs get-queue-attributes --queue-url "$url" --attribute-names RedrivePolicy VisibilityTimeout | python3 -c 'import json,os,sys; a=json.load(sys.stdin)["Attributes"]; p=json.loads(a["RedrivePolicy"]); assert p["deadLetterTargetArn"]==os.environ["EXPECTED_DLQ_ARN"]; assert int(p["maxReceiveCount"])==5; assert a["VisibilityTimeout"]=="30"'
awslocal sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names MessageRetentionPeriod | python3 -c 'import json,sys; assert json.load(sys.stdin)["Attributes"]["MessageRetentionPeriod"]=="1209600"'
awslocal sqs send-message --queue-url "$url" --message-body init-preserved >/dev/null
SQS_MAX_RECEIVE_COUNT=3 sh "$script_dir/init-aws.sh"
awslocal sqs get-queue-attributes --queue-url "$url" --attribute-names RedrivePolicy | python3 -c 'import json,sys; assert int(json.loads(json.load(sys.stdin)["Attributes"]["RedrivePolicy"])["maxReceiveCount"])==3'
awslocal sqs receive-message --queue-url "$url" --wait-time-seconds 1 | python3 -c 'import json,sys; assert json.load(sys.stdin)["Messages"][0]["Body"]=="init-preserved"'
for count in '' 0 -1 abc 01 1001; do
    if SQS_MAX_RECEIVE_COUNT="$count" sh "$script_dir/init-aws.sh" >/dev/null 2>&1; then
        echo 'Invalid receive count accepted' >&2; exit 1
    fi
done
if SQS_DLQ_NAME='' sh "$script_dir/init-aws.sh" >/dev/null 2>&1; then exit 1; fi
if SQS_DLQ_NAME="$SQS_QUEUE_NAME" sh "$script_dir/init-aws.sh" >/dev/null 2>&1; then exit 1; fi
echo 'Queue initialization tests passed'
