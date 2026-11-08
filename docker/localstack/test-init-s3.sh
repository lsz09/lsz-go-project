#!/bin/sh
set -eu

export S3_BUCKET_NAME="cloudqueue-init-s3-test-$(date +%s)-$$"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cleanup() {
    awslocal s3api delete-object --bucket "$S3_BUCKET_NAME" --key preserved >/dev/null 2>&1 || true
    awslocal s3api delete-bucket --bucket "$S3_BUCKET_NAME" >/dev/null 2>&1 || true
    if [ -n "${fake_dir-}" ]; then rm -f "$fake_dir/awslocal"; rmdir "$fake_dir"; fi
}
trap cleanup EXIT
sh "$script_dir/init-s3.sh"
awslocal s3api put-object --bucket "$S3_BUCKET_NAME" --key preserved --body "$script_dir/init-s3.sh" >/dev/null
sh "$script_dir/init-s3.sh"
awslocal s3api head-object --bucket "$S3_BUCKET_NAME" --key preserved >/dev/null
if S3_BUCKET_NAME='' sh "$script_dir/init-s3.sh" >/dev/null 2>&1; then exit 1; fi
if S3_BUCKET_NAME='../invalid' sh "$script_dir/init-s3.sh" >/dev/null 2>&1; then exit 1; fi
# A failing list (e.g. credentials/connection) must not be treated as success.
fake_dir=$(mktemp -d)
printf '#!/bin/sh\nexit 42\n' > "$fake_dir/awslocal"
chmod +x "$fake_dir/awslocal"
if PATH="$fake_dir:$PATH" sh "$script_dir/init-s3.sh" >/dev/null 2>&1; then
    echo 'S3 initialization hid a service error' >&2; exit 1
fi
echo 'S3 initialization tests passed'
