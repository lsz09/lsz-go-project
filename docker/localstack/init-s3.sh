#!/bin/sh
set -eu

bucket_name=${S3_BUCKET_NAME-cloudqueue-files}
region=${AWS_DEFAULT_REGION-ap-northeast-2}
export bucket_name
python3 -c 'import os,re,ipaddress; n=os.environ["bucket_name"]; assert re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]",n) and not any(x in n for x in ("..",".-","-.")), "Invalid S3 bucket name"
try: ipaddress.ip_address(n)
except ValueError: pass
else: raise ValueError("IP address is not a bucket name")'
case "$region" in ''|*[!a-zA-Z0-9-]*) echo 'Invalid AWS region' >&2; exit 1 ;; esac

# A failed authenticated list is a real initialization failure, not evidence that
# a bucket exists. Keep existing buckets and objects; create only when absent.
buckets=$(awslocal s3api list-buckets --query 'Buckets[].Name' --output text)
for existing in $buckets; do
    if [ "$existing" = "$bucket_name" ]; then exit 0; fi
done
if [ "$region" = us-east-1 ]; then
    awslocal s3api create-bucket --bucket "$bucket_name" --region "$region" >/dev/null
else
    awslocal s3api create-bucket --bucket "$bucket_name" --region "$region" --create-bucket-configuration "LocationConstraint=$region" >/dev/null
fi
