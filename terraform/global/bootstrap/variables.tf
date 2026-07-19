# terraform/global/bootstrap/variables.tf
#
# Extracted from aws_backend.tf during the pre-Milestone-3 file-structure
# refactor. Content moved verbatim, unchanged. Milestone 3 will add its own
# GCP-specific variables here rather than creating a parallel file.

variable "aws_region" {
  description = "AWS region that hosts the Terraform state bucket and lock table. This is a single global region for this account-level singleton — it does not need to match every environment's compute region, though colocating avoids a (negligible) cross-region latency cost on state reads/writes. Revisit once Milestone 9 picks the EKS region."
  type        = string
  default     = "us-east-1"

  validation {
    condition     = can(regex("^[a-z]{2}-[a-z]+-\\d$", var.aws_region))
    error_message = "aws_region must look like a real AWS region identifier, e.g. us-east-1."
  }
}

variable "project_name" {
  description = "Short, S3-safe project identifier used as a prefix for every resource this module creates. Every other AWS Terraform root re-derives the bucket/table name from this same default rather than importing this module directly (root modules don't call each other) — keep this value identical everywhere it's copied, and see README.md for the resulting naming convention."
  type        = string
  default     = "pulse-platform"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}[a-z0-9]$", var.project_name))
    error_message = "project_name must be lowercase alphanumeric with hyphens, 3-32 characters — it is used directly in the S3 bucket name, which has its own naming restrictions."
  }
}
