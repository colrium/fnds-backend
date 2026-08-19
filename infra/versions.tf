terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  backend "s3" {
    bucket       = "fundis-terraform-state"
    region       = "af-south-1"
    use_lockfile = true
    encrypt      = true
    # key is intentionally omitted — supplied at init time via:
    #   terraform init -backend-config="key=backend/staging/terraform.tfstate"
    # so the same file works for every environment this repo deploys to.
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Environment = var.environment
      Project     = var.project_name
      ManagedBy   = "terraform"
      Service     = "backend"
    }
  }
}
