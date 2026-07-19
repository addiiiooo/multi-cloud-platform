# terraform/global/bootstrap/outputs.tf
#
# Extracted from aws_backend.tf during the pre-Milestone-3 file-structure
# refactor. Content moved verbatim, unchanged — every output keeps its
# original name and value expression exactly, so nothing that already
# consumes these outputs (or will, in later milestones) is affected.

# -----------------------------------------------------------------------------
# Outputs — every other AWS Terraform root's backend configuration is built
# from these values instead of a hand-copied bucket/table name.
# -----------------------------------------------------------------------------

output "state_bucket_name" {
  description = "Name of the S3 bucket every AWS-side Terraform root should use as its remote state `bucket`."
  value       = aws_s3_bucket.terraform_state.bucket
}

output "state_bucket_arn" {
  description = "ARN of the state bucket — reference this when scoping any future IAM policy to it, rather than reconstructing the ARN string by hand."
  value       = aws_s3_bucket.terraform_state.arn
}

output "lock_table_name" {
  description = "Name of the DynamoDB table every AWS-side Terraform root should use as its remote state `dynamodb_table`."
  value       = aws_dynamodb_table.terraform_locks.name
}

output "lock_table_arn" {
  description = "ARN of the DynamoDB lock table."
  value       = aws_dynamodb_table.terraform_locks.arn
}

output "terraform_backend_access_policy_arn" {
  description = "ARN of the least-privilege IAM policy granting exactly the S3 + DynamoDB permissions needed to use this backend. Attach this to whatever role needs it (e.g. Milestone 18's CI apply role) instead of writing an equivalent policy by hand."
  value       = aws_iam_policy.terraform_backend_access.arn
}

output "aws_region" {
  description = "The region this backend lives in, echoed back so a consuming root module's own provider block can stay in sync with this one without hardcoding the region a second time."
  value       = var.aws_region
}

output "backend_config_example" {
  description = "A ready-to-paste backend \"s3\" block for any new AWS-side Terraform root. Copy this and change only `key` to that root's own state path — see README.md for the key-naming convention every environment root follows."
  value       = <<-EOT
    terraform {
      backend "s3" {
        bucket         = "${aws_s3_bucket.terraform_state.bucket}"
        key            = "CHANGE-ME/terraform.tfstate"
        region         = "${var.aws_region}"
        dynamodb_table = "${aws_dynamodb_table.terraform_locks.name}"
        encrypt        = true
      }
    }
  EOT
}
