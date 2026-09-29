#!/bin/sh
set -eu

awslocal sqs create-queue --queue-name "${SQS_QUEUE_NAME:-cloudqueue-jobs}" >/dev/null
