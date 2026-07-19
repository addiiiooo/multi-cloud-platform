# terraform/global/bootstrap/versions.tf
#
# Extracted from aws_backend.tf during the pre-Milestone-3 file-structure
# refactor (Category A finding from the PR review). Content moved verbatim,
# unchanged — this file exists so the shared terraform{}/provider "aws"
# blocks are not duplicated or awkwardly shared once gcp_backend.tf lands
# in this same directory in Milestone 3.

terraform {
  # 1.7.0 is the real floor: this module's own tests (tests/aws_backend.tftest.hcl)
  # use the `mock_provider` / `override_data` test-mocking framework, which
  # was introduced in Terraform 1.7.0. Pinning required_version below that
  # would let `terraform apply` succeed on an older Terraform while
  # `terraform test` silently couldn't run at all — an inconsistency worth
  # closing off explicitly rather than discovering later.
  required_version = ">= 1.7.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # No `backend` block here — see the comment above and README.md.
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project       = var.project_name
      ManagedBy     = "terraform"
      TerraformRoot = "global/bootstrap"
      # This resource is an account-level singleton, not tied to any one
      # dev/staging/prod environment — tagged accordingly so the
      # environment-scoped IAM conditions described in architecture Section 7
      # ("a compromised prod apply credential can't touch dev resources
      # tagged to a different environment") never accidentally match it.
      Environment = "global"
    }
  }
}
