# terraform/global/bootstrap/tests/aws_backend.tftest.hcl
#
# Plan-only tests for aws_backend.tf, run via `terraform test` from
# terraform/global/bootstrap/. Every run block below uses a fully mocked AWS
# provider, so this file needs no real AWS credentials, no network access,
# and creates no real cloud resources or cost — appropriate for a module
# whose whole design point is that it's applied by a human, once, not
# routinely re-run by CI (see ../README.md).
#
# Known, deliberate limitation, disclosed rather than hidden: because the
# entire "aws" provider is mocked, every COMPUTED attribute (arn, id, and —
# importantly — data.aws_iam_policy_document's own `json` output) is
# replaced with provider-generated fake data instead of this configuration's
# real, rendered value. That means these tests can verify every argument
# THIS configuration sets directly — bucket naming, versioning status,
# encryption algorithm, public-access-block flags, ownership controls,
# billing mode, hash key, and the IAM policy resource's name/description —
# but they cannot verify the actual rendered IAM policy JSON content, since
# that content is itself a mocked/computed value under this test setup. That
# specific check happens by reading real `terraform plan` output a human
# runs locally (see ../README.md's verification steps) — noted here rather
# than writing an assertion that looks like it checks policy content but
# silently doesn't.
#
# Set-typed nested blocks: under Terraform 1.15 / hashicorp/aws ~> 6.0,
# `aws_s3_bucket_versioning.versioning_configuration`,
# `aws_s3_bucket_server_side_encryption_configuration.rule` (and, one level
# further in, its own nested `apply_server_side_encryption_by_default`), and
# `aws_s3_bucket_ownership_controls.rule` are all Set-typed, not List-typed,
# even though each is constrained to exactly one block in practice. Sets
# have no defined order, so `foo.bar[0]` is invalid HCL against them —
# `one(foo.bar)` is used instead everywhere below: it asserts "there is
# exactly one element" and returns it, which is both what actually type-
# checks against a set and a more precise expression of intent than
# `tolist(foo.bar)[0]` (which would silently tolerate — and pick an
# unspecified element from — an unexpected second block instead of erroring
# on it). Where a Set-typed block sits inside another Set-typed block —
# `apply_server_side_encryption_by_default` inside `rule` — `one()` is
# nested once per level: `one(one(rule).apply_server_side_encryption_by_default).sse_algorithm`,
# confirmed necessary by an actual `terraform test` run rather than assumed;
# an earlier version of this file guessed that inner block was a plain
# object needing no unwrapping, which a real run showed to be wrong.

mock_provider "aws" {
  override_data {
    target = data.aws_caller_identity.current
    values = {
      account_id = "123456789012"
    }
  }
}

run "state_bucket_name_is_account_scoped" {
  command = plan

  assert {
    condition     = aws_s3_bucket.terraform_state.bucket == "pulse-platform-tfstate-123456789012"
    error_message = "state bucket name must be \"<project_name>-tfstate-<account_id>\" so it is globally unique without a human having to pick and verify a name"
  }
}

run "state_bucket_name_respects_project_name_override" {
  command = plan

  variables {
    project_name = "custom-project"
  }

  assert {
    condition     = aws_s3_bucket.terraform_state.bucket == "custom-project-tfstate-123456789012"
    error_message = "state bucket name must follow project_name overrides, not just the default"
  }
}

run "versioning_is_enabled" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_versioning.terraform_state.versioning_configuration).status == "Enabled"
    error_message = "state bucket must have versioning Enabled so a bad state write can be rolled back (architecture Section 12)"
  }
}

run "encryption_uses_kms_with_bucket_keys" {
  command = plan

  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.terraform_state.rule).apply_server_side_encryption_by_default).sse_algorithm == "aws:kms"
    error_message = "state bucket must be SSE-KMS encrypted per Milestone 2's spec and architecture Section 7"
  }

  assert {
    condition     = one(aws_s3_bucket_server_side_encryption_configuration.terraform_state.rule).bucket_key_enabled == true
    error_message = "S3 Bucket Keys should stay enabled to reduce KMS request cost — free to enable, no downside"
  }
}

run "public_access_is_fully_blocked" {
  command = plan

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.terraform_state.block_public_acls,
      aws_s3_bucket_public_access_block.terraform_state.block_public_policy,
      aws_s3_bucket_public_access_block.terraform_state.ignore_public_acls,
      aws_s3_bucket_public_access_block.terraform_state.restrict_public_buckets,
    ])
    error_message = "all four public access block settings must be true — the state bucket must never be publicly reachable"
  }
}

run "bucket_ownership_is_enforced" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_ownership_controls.terraform_state.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "bucket must disable ACLs entirely (BucketOwnerEnforced), not just discourage them"
  }
}

run "lock_table_uses_on_demand_billing" {
  command = plan

  assert {
    condition     = aws_dynamodb_table.terraform_locks.billing_mode == "PAY_PER_REQUEST"
    error_message = "lock table traffic is low-volume and bursty — on-demand billing avoids under- or over-provisioning capacity"
  }
}

run "lock_table_hash_key_matches_backend_requirement" {
  command = plan

  assert {
    condition     = aws_dynamodb_table.terraform_locks.hash_key == "LockID"
    error_message = "Terraform's S3 backend requires the lock table's hash key to be named exactly \"LockID\" — this is not a stylistic choice"
  }
}

run "backend_access_policy_is_named_per_project" {
  command = plan

  assert {
    condition     = aws_iam_policy.terraform_backend_access.name == "pulse-platform-terraform-backend-access"
    error_message = "backend-access policy name must be derived from project_name so it stays identifiable if this module is ever reused for another project"
  }
}
