# terraform/global/bootstrap/aws_backend.tf
#
# Milestone 2 — Terraform Remote State Bootstrap: AWS.
# Implements architecture Section 10 ("Terraform Structure" -> global/bootstrap/
# -> aws_backend.tf: "S3 bucket (versioned, encrypted) + DynamoDB lock table"),
# Section 7 ("Encryption": "S3 and GCS buckets, including Terraform state,
# default-encrypted with versioning enabled"), and Section 12 ("Terraform
# state corruption": "S3 and GCS both have object versioning enabled...
# state locking (DynamoDB on AWS...) prevents the concurrent-write race that
# causes corruption in the first place").
#
# This is the ONE Terraform root in the entire repository that does not, and
# cannot, use a remote backend. Every other root's backend.tf will point at
# the S3 bucket and DynamoDB table this file creates — so this file's own
# state has nowhere to live but locally, on whoever runs it. See README.md's
# "Why local state" section for how that local state file should be handled.
#
# GCP's equivalent (gcp_backend.tf, using GCS) is Milestone 3 and is
# deliberately NOT included here — this milestone is AWS-only.
#
# NOTE (post-Category-A-refactor): this file now holds only the data
# sources and resources. terraform{}/provider config moved to versions.tf,
# both variables moved to variables.tf, all outputs moved to outputs.tf.
# Nothing below changed — same resource addresses, same argument values,
# same variable/output names. See ../README.md for the full file list.

# -----------------------------------------------------------------------------
# Data sources
# -----------------------------------------------------------------------------

# Used to suffix the state bucket's name with the AWS account ID. S3 bucket
# names are globally unique across every AWS account on the planet, not just
# within this account — a plausible-sounding name like "pulse-platform-tfstate"
# could already be taken by a stranger. Account IDs are unique by definition,
# so this guarantees a collision-free name deterministically, without a
# human having to hand-pick and verify one, and without pulling in the
# `random` provider just to generate a suffix that would then need to be
# recorded somewhere to survive a state loss.
data "aws_caller_identity" "current" {}

# -----------------------------------------------------------------------------
# S3 bucket: Terraform remote state storage
# -----------------------------------------------------------------------------

resource "aws_s3_bucket" "terraform_state" {
  bucket = "${var.project_name}-tfstate-${data.aws_caller_identity.current.account_id}"

  # Every other Terraform root in this repository will eventually point its
  # backend at this exact bucket. Destroying it is a deliberate, manual,
  # out-of-band decision that must never be a side effect of some unrelated
  # `terraform destroy` run elsewhere in the repo.
  lifecycle {
    prevent_destroy = true
  }
}

# Versioning is what makes "Terraform state corruption" (architecture
# Section 12) a recoverable event instead of a data-loss incident: a bad
# `apply` that writes a corrupted or unwanted state file can be rolled back
# to the prior object version instead of being unrecoverable.
resource "aws_s3_bucket_versioning" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id
  versioning_configuration {
    status = "Enabled"
  }
}

# SSE-KMS, using the AWS-managed `aws/s3` key rather than a new
# customer-managed key (CMK). Reasoning, spelled out because it's a genuine
# tradeoff and not a default: this bucket holds Terraform state, and per this
# project's design (architecture Section 12) state never contains a secret
# VALUE — only references (ARNs/IDs) to entries in Secrets Manager. A
# customer-managed KMS key would add ~$1/month plus a key policy to author
# and rotate, for a bucket that isn't protecting anything a CMK's extra
# access controls would meaningfully improve on. The AWS-managed key still
# satisfies "encrypted with KMS" (distinct from, and stronger than, plain
# SSE-S3/AES256 — every decrypt is visible in CloudTrail), and its default
# key policy already permits any principal with S3 permissions on the object
# to use it within this account, so the IAM policy below needs no separate
# kms:Decrypt/kms:GenerateDataKey grant. Switching to a CMK later, if a
# stricter compliance posture ever requires one, is a one-line change
# (`kms_master_key_id = aws_kms_key.xxx.arn`).
resource "aws_s3_bucket_server_side_encryption_configuration" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "aws:kms"
    }
    # Reduces per-request calls to KMS (and their cost) by caching a data key
    # at the bucket level for a short period. Free to enable, no downside,
    # only applicable to SSE-KMS (which is why it wasn't set alongside
    # plain SSE-S3 in earlier drafts of this file).
    bucket_key_enabled = true
  }
}

# All four settings enabled: this bucket must never be reachable via any
# public ACL or bucket policy, under any future misconfiguration. This is
# the "public access blocked" requirement from Milestone 2's own spec, and
# directly implements architecture Section 7's container/network-isolation
# posture applied to storage instead of compute.
resource "aws_s3_bucket_public_access_block" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Disables ACLs entirely in favor of bucket policies only (the
# "BucketOwnerEnforced" posture AWS has recommended as the default since
# April 2023). Paired with the public access block above, this makes
# "someone grants public access via a stray ACL" structurally impossible
# rather than merely blocked-but-still-possible-to-attempt.
resource "aws_s3_bucket_ownership_controls" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

# -----------------------------------------------------------------------------
# DynamoDB table: state locking
# -----------------------------------------------------------------------------

# Prevents the concurrent-write race referenced in architecture Section 12:
# two `terraform apply` runs against the same state key can never interleave,
# because the second one blocks (or fails fast) waiting for the lock item
# the first one holds. `LockID` as the hash key is not a stylistic choice —
# it's the exact attribute name Terraform's S3 backend requires.
resource "aws_dynamodb_table" "terraform_locks" {
  name         = "${var.project_name}-tfstate-lock"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  # Same reasoning as the state bucket: losing this table breaks locking for
  # every AWS-side Terraform root in the repository, so destroying it must
  # be a deliberate, manual, out-of-band action.
  lifecycle {
    prevent_destroy = true
  }
}

# -----------------------------------------------------------------------------
# IAM: least-privilege access to this backend
# -----------------------------------------------------------------------------

# Scoped to exactly what `terraform init`/`plan`/`apply` needs against THIS
# bucket and THIS table — nothing broader. This is the concrete artifact the
# architecture's own Principal Engineer review (Section 16, point 3) asked
# for explicitly: "Show me the actual policy JSON," not an assertion that
# least privilege was implemented. Future milestones that need a role
# capable of running Terraform (e.g. Milestone 18's CI apply role) should
# attach this policy rather than hand-writing an equivalent one — a second,
# slightly-different copy of this policy is exactly the kind of drift the
# architecture's Section 16 warns about.
#
# s3:DeleteObject is deliberately NOT included: standard init/plan/apply
# never deletes a state object outright (a "deleted" resource just becomes
# an empty/updated state write). If a future workflow genuinely needs it
# (e.g. decommissioning an entire environment's state key), grant it there,
# scoped to that specific need, rather than broadening this shared policy.
data "aws_iam_policy_document" "terraform_backend_access" {
  statement {
    sid       = "ListStateBucket"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.terraform_state.arn]
  }

  statement {
    sid       = "ReadWriteStateObjects"
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.terraform_state.arn}/*"]
  }

  statement {
    sid    = "UseStateLockTable"
    effect = "Allow"
    actions = [
      "dynamodb:DescribeTable",
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:DeleteItem",
    ]
    resources = [aws_dynamodb_table.terraform_locks.arn]
  }
}

resource "aws_iam_policy" "terraform_backend_access" {
  name        = "${var.project_name}-terraform-backend-access"
  description = "Least-privilege permissions to read/write Terraform state in ${aws_s3_bucket.terraform_state.id} and acquire/release locks in ${aws_dynamodb_table.terraform_locks.name}. Attach to any role that runs terraform init/plan/apply against this backend."
  policy      = data.aws_iam_policy_document.terraform_backend_access.json
}
