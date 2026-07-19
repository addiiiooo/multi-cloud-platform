# `terraform/global/bootstrap` — AWS remote state

Milestone 2 of the platform's implementation roadmap. This directory creates the one thing every other AWS Terraform root in this repository needs before it can run: an S3 bucket to hold remote state, and a DynamoDB table to lock it during writes.

GCP's equivalent (a GCS bucket, using `gcp_backend.tf`) is Milestone 3 and is **not** part of this milestone. It will be added to this same directory later, per architecture Section 10, adding its own resources to `gcp_backend.tf` and its own variables/outputs to the shared `variables.tf`/`outputs.tf` files below — without changing anything in `aws_backend.tf`.

## File layout

Split into four files ahead of Milestone 3 (a pre-M3 refactor, not part of this milestone's original scope — see the PR review that identified it):

- **`versions.tf`** — `terraform {}` block and the `provider "aws" {}` block. Kept separate so Milestone 3's `google` provider requirement has an obvious, shared home instead of duplicating this block or bolting onto a cloud-specific file.
- **`variables.tf`** — `aws_region`, `project_name`. Milestone 3 adds its own GCP-specific variables here.
- **`aws_backend.tf`** — the actual AWS resources: the S3 bucket and its four sub-resources, the DynamoDB table, and the IAM policy. Kept under this exact name because both the architecture document (Section 10) and Milestone 2's own spec name it specifically.
- **`outputs.tf`** — all seven outputs. Milestone 3 adds its own GCP-specific outputs here.

This split changed nothing about behavior — same resource addresses, same variable/output names, same argument values, verified byte-for-byte against the pre-refactor version. See the PR history for the full before/after diff.

## Why this directory is different from every other Terraform root

Every other root module in this repository (`terraform/environments/*/aws/...`) will configure a `backend "s3" {}` block pointing at the bucket and table this directory creates. This directory can't do that for itself — it's the thing that creates the backend, so at the moment it first runs, the backend doesn't exist yet. This is a standard, well-known bootstrapping problem in Terraform, and the standard resolution is the one used here: this root uses Terraform's default **local** state (a `terraform.tfstate` file written next to `aws_backend.tf`) and is applied manually, once, by a human — never by CI, and never automatically alongside every other `terraform apply`.

## What this creates

**S3 bucket** (`aws_s3_bucket.terraform_state`), named `<project_name>-tfstate-<aws_account_id>` — the account ID suffix guarantees a globally-unique name deterministically, without a human having to hand-pick and verify one against S3's account-independent global namespace.

- **Versioning: enabled.** A bad or corrupted state write becomes a recoverable event (roll back to the prior object version) instead of data loss — this is the direct mechanism behind architecture Section 12's "Terraform state corruption" recovery story.
- **Encryption: SSE-KMS, using the AWS-managed `aws/s3` key** (not a new customer-managed key). This satisfies Milestone 2's "SSE-KMS encrypted" requirement and architecture Section 7's encryption-at-rest requirement, while avoiding the ~$1/month cost and key-policy-authoring overhead of a customer-managed key (CMK) for a bucket that — by this project's own design — never holds a secret value, only references (ARNs/IDs) to entries in Secrets Manager. Every decrypt is still visible in CloudTrail. Upgrading to a CMK later, if a stricter compliance posture ever requires it, is a one-line change.
- **Public access: fully blocked**, all four settings (`block_public_acls`, `block_public_policy`, `ignore_public_acls`, `restrict_public_buckets`). Paired with `BucketOwnerEnforced` ownership controls, which disable bucket ACLs entirely — public access here is structurally impossible, not just discouraged.
- **`prevent_destroy = true`.** Every other AWS root's state will eventually live in this bucket; destroying it must always be a deliberate, manual, out-of-band decision.

**DynamoDB table** (`aws_dynamodb_table.terraform_locks`), named `<project_name>-tfstate-lock`, hash key `LockID` (the exact attribute name Terraform's S3 backend requires — not a stylistic choice), billing mode `PAY_PER_REQUEST` (lock-table traffic is a handful of reads/writes per `apply`, bursty and low-volume — on-demand billing avoids guessing at provisioned capacity). Also `prevent_destroy = true`, for the same reason as the bucket.

**IAM policy** (`aws_iam_policy.terraform_backend_access`) — least-privilege permissions to run `terraform init`/`plan`/`apply` against this specific bucket and table: `s3:ListBucket` on the bucket, `s3:GetObject`/`s3:PutObject` on its contents, and `dynamodb:DescribeTable`/`GetItem`/`PutItem`/`DeleteItem` on the lock table. `s3:DeleteObject` is deliberately omitted — ordinary `init`/`plan`/`apply` never needs it, and adding permissions "just in case" is exactly the drift the architecture's own Principal Engineer review (Section 16) called out. Future milestones that need a role capable of running Terraform (notably Milestone 18's CI apply role) should attach this policy rather than writing an equivalent one by hand.

## Prerequisites

- Terraform >= 1.7.0 (see `versions.tf`'s `required_version` — the floor is 1.7.0, not something older, specifically because this module's own tests use the provider-mocking framework introduced in that version).
- AWS provider `~> 6.0` (downloaded automatically by `terraform init`).
- AWS credentials for the target account, with enough privilege to create S3 buckets, DynamoDB tables, and IAM policies (an admin or bootstrap-specific role — this is the one time in the whole repository a broad credential is the correct tool, precisely because it's what creates the narrow one used everywhere else).

## Running it (once)

```bash
cd terraform/global/bootstrap
terraform init
terraform plan   # review before applying — this creates real, billable (if trivial-cost) resources
terraform apply
```

There is no `-backend-config` step, no workspace selection, and nothing else to configure — that's the point of this being the bootstrap root.

## Handling the local state file afterward

`terraform apply` above will write a `terraform.tfstate` file into this directory. Unlike every other root in this repository, **this specific file should be committed to version control**, deliberately, as an explicit exception to the usual "never commit `.tfstate`" rule:

- It contains no secret values — only resource metadata (bucket name, table ARN, IAM policy ARN, etc.). Per architecture Section 12, this project never stores a secret value in Terraform state anywhere, so this file is no more sensitive here than elsewhere.
- Losing it would orphan real AWS resources: the next `terraform apply` run without the old state would try to create a second bucket/table rather than recognizing the existing ones (or fail outright on the now-taken bucket name), and recovering would mean manually running `terraform import` against every resource in this file — solvable, but a genuinely avoidable operational headache for the one root that can't fall back to "just re-read it from the remote backend," because it *is* the remote backend.

If a repository-wide `.gitignore` is added in a later milestone (Milestone 0's scaffolding), it must carve out an explicit exception for `terraform/global/bootstrap/terraform.tfstate*` — noted here so that milestone doesn't silently re-break this one.

## How other Terraform roots consume this

Run `terraform output backend_config_example` from this directory after applying. It prints a ready-to-paste `backend "s3" {}` block:

```hcl
terraform {
  backend "s3" {
    bucket         = "pulse-platform-tfstate-<your-account-id>"
    key            = "CHANGE-ME/terraform.tfstate"
    region         = "us-east-1"
    dynamodb_table = "pulse-platform-tfstate-lock"
    encrypt        = true
  }
}
```

Every future AWS root copies this block and changes only `key`, following architecture Section 10's convention — for example `envs/staging/aws/terraform.tfstate` for Milestone 7's staging network root. The bucket and table themselves are shared across every environment; only the object key differs, which is what keeps each environment's state physically isolated within one bucket.

The other outputs (`state_bucket_name`, `state_bucket_arn`, `lock_table_name`, `lock_table_arn`, `terraform_backend_access_policy_arn`, `aws_region`) exist so later modules can reference exact values instead of re-typing strings by hand — most importantly `terraform_backend_access_policy_arn`, which Milestone 18's CI apply role should attach directly.

## Verification

1. **Versioning is enabled:**
   ```bash
   aws s3api get-bucket-versioning --bucket "$(terraform output -raw state_bucket_name)"
   ```
   Expect `{"Status": "Enabled"}`.

2. **Public access is blocked:**
   ```bash
   aws s3api get-public-access-block --bucket "$(terraform output -raw state_bucket_name)"
   ```
   Expect all four settings `true`.

3. **Encryption is SSE-KMS:**
   ```bash
   aws s3api get-bucket-encryption --bucket "$(terraform output -raw state_bucket_name)"
   ```
   Expect `"SSEAlgorithm": "aws:kms"`.

4. **A throwaway root can actually use this backend and locks correctly** — the milestone's own acceptance test. From a scratch directory:
   ```bash
   mkdir -p /tmp/backend-smoke-test && cd /tmp/backend-smoke-test
   cat > main.tf <<'EOF'
   terraform {
     backend "s3" {
       bucket         = "REPLACE-WITH-state_bucket_name-OUTPUT"
       key            = "smoke-test/terraform.tfstate"
       region         = "us-east-1"
       dynamodb_table = "REPLACE-WITH-lock_table_name-OUTPUT"
       encrypt        = true
     }
   }
   resource "null_resource" "smoke_test" {}
   EOF
   terraform init
   terraform apply -auto-approve
   ```
   `terraform init` must succeed against the real backend, and while `apply` is running, `aws dynamodb scan --table-name "$(terraform output -raw lock_table_name 2>/dev/null || echo pulse-platform-tfstate-lock)"` (run from a second terminal, mid-apply) should show a row keyed by the state's `LockID`. Run `terraform destroy -auto-approve` afterward to clean up the smoke-test resource and its state object — the bucket and table themselves are untouched either way.

## Running the automated tests

```bash
cd terraform/global/bootstrap
terraform test
```

`tests/aws_backend.tftest.hcl` runs entirely against a mocked AWS provider (`mock_provider "aws"` with an `override_data` block supplying a fixed fake account ID) — no real AWS credentials, network access, or billable resources are involved. It checks bucket naming (including that it correctly follows a `project_name` override), versioning, encryption algorithm and bucket-key setting, all four public-access-block flags, bucket ownership controls, DynamoDB billing mode and hash key, and the IAM policy's name. See the comment at the top of that file for the one thing it deliberately can't check under mocking (the actual rendered IAM policy JSON content) and why — that's covered by the `terraform plan` review in step 3 above instead.

## Known limitations / TODOs

- **DynamoDB-based locking is now the deprecated path in current Terraform (1.11+).** As of mid-2026, HashiCorp's own S3 backend documentation marks `dynamodb_table`-based locking as deprecated in favor of S3-native conditional-write locking (`use_lockfile = true`, which needs no DynamoDB table at all). This module still uses DynamoDB deliberately, because it's what the approved architecture (Section 10's file tree, Section 12's failure-scenario writeup) and Milestone 2's own spec explicitly and repeatedly name, and DynamoDB locking still works correctly today — it is discouraged, not broken. Migrating to `use_lockfile` would be a real, defensible future improvement (simpler, slightly cheaper, fewer IAM permissions to manage) but is an architecture-level change this milestone deliberately does not make unilaterally.
- **Region (`us-east-1` default) is not yet reconciled against where EKS actually lands.** Milestone 9 picks the real AWS region for compute; this bootstrap's region should be revisited then, though a cross-region state bucket is fully functional, just a (negligible) latency nit, not a correctness problem.
- **Not yet scanned by tfsec/checkov.** The `terraform/policy/` tooling that runs those checks in CI is Milestone 0/23's responsibility, not this one's — this module hasn't been machine-verified against a policy-as-code rule set yet, only hand-reviewed against the architecture's stated security requirements.
- **No destroy runbook.** Tearing down this bootstrap (if the project were ever fully decommissioned) requires manually removing `prevent_destroy` from both resources first, then emptying the versioned bucket of every object version before `terraform destroy` can succeed. Not written up yet because nothing in the roadmap needs it before Milestone 23 at the earliest.
- **Sandbox disclosure:** this file and its test were authored and statically reviewed in an environment without a Terraform binary or AWS network access available (the same constraint noted throughout this project's Go milestones) — `terraform fmt`, `terraform validate`, and `terraform test` have not actually been executed against this code. Please run all three locally as your first verification step, before the AWS-specific checks above.
