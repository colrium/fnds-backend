locals {
  platform_ssm_prefix = "/${var.project_name}/${var.environment}/platform"
}

data "aws_ssm_parameter" "private_subnet_ids" {
  name = "${local.platform_ssm_prefix}/private-subnet-ids"
}

data "aws_ssm_parameter" "cluster_arn" {
  name = "${local.platform_ssm_prefix}/cluster-arn"
}

data "aws_ssm_parameter" "task_execution_role_arn" {
  name = "${local.platform_ssm_prefix}/execution-role-arn"
}

data "aws_ssm_parameter" "task_role_arn" {
  name = "${local.platform_ssm_prefix}/task-role-arn"
}

data "aws_ssm_parameter" "task_execution_role_name" {
  name = "${local.platform_ssm_prefix}/execution-role-name"
}

data "aws_ssm_parameter" "task_role_name" {
  name = "${local.platform_ssm_prefix}/task-role-name"
}

data "aws_ssm_parameter" "ecs_security_group_id" {
  name = "${local.platform_ssm_prefix}/ecs-security-group-id"
}

data "aws_ssm_parameter" "target_group_arn_backend" {
  name = "${local.platform_ssm_prefix}/target-group-arn/backend"
}

data "aws_ssm_parameter" "redis_url" {
  name            = "${local.platform_ssm_prefix}/redis-url"
  with_decryption = true
}

data "aws_ssm_parameter" "mongodb_uri" {
  name            = "${local.platform_ssm_prefix}/mongodb-uri"
  with_decryption = true
}

data "aws_ssm_parameter" "s3_bucket_name" {
  name = "${local.platform_ssm_prefix}/s3-bucket-name"
}

data "aws_ssm_parameter" "s3_bucket_arn" {
  name = "${local.platform_ssm_prefix}/s3-bucket-arn"
}

data "aws_ssm_parameter" "s3_public_base_url" {
  name = "${local.platform_ssm_prefix}/s3-public-base-url"
}

data "aws_ssm_parameter" "s3_bucket_region" {
  name = "${local.platform_ssm_prefix}/s3-bucket-region"
}
