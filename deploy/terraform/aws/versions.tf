terraform {
  required_version = ">= 1.9"
  required_providers {
    aws    = { source = "hashicorp/aws", version = "~> 6.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
  # Remote state with locking; create the bucket once per account (bootstrap is out of scope here).
  # backend "s3" {
  #   bucket       = "your-org-terraform-state"
  #   key          = "sentinel/aws.tfstate"
  #   region       = "eu-west-1"
  #   use_lockfile = true
  # }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "sentinel", Environment = var.environment, ManagedBy = "terraform" }
  }
}
