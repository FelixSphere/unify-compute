#!/usr/bin/env bash
# Creates the S3 bucket that holds Terraform state for the UnifyAI account.
# Idempotent: safe to re-run, and it will not weaken a bucket that exists.
#
# Run once per account, before the first `terraform init` of any stack that
# declares the s3 backend. Bucket name and region must match backend.tf.
set -euo pipefail

BUCKET="${TF_STATE_BUCKET:-unifyai-tfstate-871376691052}"
REGION="${AWS_REGION:-us-east-1}"

account=$(aws sts get-caller-identity --query Account --output text)
echo "account $account  region $REGION  bucket $BUCKET"

if aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
  echo "bucket exists"
else
  # us-east-1 is the one region that rejects a LocationConstraint.
  if [ "$REGION" = "us-east-1" ]; then
    aws s3api create-bucket --bucket "$BUCKET" --region "$REGION"
  else
    aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
      --create-bucket-configuration "LocationConstraint=$REGION"
  fi
  echo "bucket created"
fi

# Versioning is what makes a corrupted or truncated state recoverable. It is
# the reason this bucket is worth having at all; set it before anything writes.
aws s3api put-bucket-versioning --bucket "$BUCKET" \
  --versioning-configuration Status=Enabled

aws s3api put-bucket-encryption --bucket "$BUCKET" \
  --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'

aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration \
  'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'

# State files are small; keeping every version forever is cheap, but keeping
# every *delete marker* forever is clutter. Expire noncurrent versions after a
# year — long enough that any recovery anyone would attempt is still possible.
aws s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" \
  --lifecycle-configuration '{"Rules":[{"ID":"expire-noncurrent","Status":"Enabled","Filter":{},"NoncurrentVersionExpiration":{"NoncurrentDays":365},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}]}'

echo
echo "verifying:"
printf '  versioning   %s\n' "$(aws s3api get-bucket-versioning --bucket "$BUCKET" --query Status --output text)"
printf '  encryption   %s\n' "$(aws s3api get-bucket-encryption --bucket "$BUCKET" \
  --query 'ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm' --output text)"
printf '  public block %s\n' "$(aws s3api get-public-access-block --bucket "$BUCKET" \
  --query 'PublicAccessBlockConfiguration.BlockPublicPolicy' --output text)"
echo
echo "next: terraform -chdir=deploy/aws/staging init"
