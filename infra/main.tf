locals {
  name_prefix = "${var.project_name}-${var.environment}-backend"
  mongodb_uri = var.mongodb_uri_override != "" ? var.mongodb_uri_override : data.aws_ssm_parameter.mongodb_uri.value

  backend_secret_candidates = {
    MONGODB_URI                     = local.mongodb_uri
    SESSION_KEY                     = var.session_key
    SENDGRID_API_KEY                = var.sendgrid_api_key
    FLUTTERWAVE_SECRET_KEY          = var.flutterwave_secret_key
    FLUTTERWAVE_WEBHOOK_SECRET_HASH = var.flutterwave_webhook_secret_hash
    CELLULANT_API_KEY               = var.cellulant_api_key
    CELLULANT_ACCESS_KEY            = var.cellulant_access_key
    CELLULANT_SECRET_KEY            = var.cellulant_secret_key
    CELLULANT_IV_KEY                = var.cellulant_iv_key
    CELLULANT_CLIENT_ID             = var.cellulant_client_id
    CELLULANT_CLIENT_SECRET         = var.cellulant_client_secret
    CELLULANT_SERVICE_CODE          = var.cellulant_service_code
    KOPOKOPO_API_KEY                = var.kopokopo_api_key
    ADVANTA_API_KEY                 = var.advanta_api_key
    ADVANTA_PARTNER_ID              = var.advanta_partner_id
    ADVANTA_SHORTCODE               = var.advanta_shortcode
    TURNSTILE_SECRET_KEY            = var.turnstile_secret_key
    SLACK_WEBHOOK_URL               = var.slack_webhook_url
    SLACK_OPS_NOTIFS_URL            = var.slack_ops_notifs_url
    BETTERSTACK_SOURCE_TOKEN        = var.betterstack_source_token
    VIDEOSDK_API_KEY                = var.videosdk_api_key
    VIDEOSDK_SECRET_KEY             = var.videosdk_secret_key
  }

  backend_secret_has_value = { for k, v in local.backend_secret_candidates : k => nonsensitive(v != "") }
  backend_secret_keys      = toset([for k, has_value in local.backend_secret_has_value : k if has_value])
  backend_secret_values    = { for k in local.backend_secret_keys : k => local.backend_secret_candidates[k] }
  backend_secret_env_vars  = { for k, has_value in local.backend_secret_has_value : k => "" if !has_value }

  backend_secret_unexpected_blanks = [
    for k, has_value in local.backend_secret_has_value :
    k if !has_value && !contains(var.unset_secrets, k)
  ]
}

resource "terraform_data" "unset_secrets_check" {
  lifecycle {
    precondition {
      condition     = length(local.backend_secret_unexpected_blanks) == 0
      error_message = "Blank secret(s) not listed in unset_secrets: ${join(", ", local.backend_secret_unexpected_blanks)}."
    }
  }
}

resource "aws_ecr_repository" "this" {
  name                 = local.name_prefix
  image_tag_mutability = "MUTABLE"
  force_delete         = true

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Keep last 10 images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 10
        }
        action = {
          type = "expire"
        }
      }
    ]
  })
}

resource "aws_ssm_parameter" "image" {
  name        = "/${var.project_name}/${var.environment}/backend/image"
  description = "Digest-pinned image deployed for backend; written by CI"
  type        = "String"
  value       = "${aws_ecr_repository.this.repository_url}@sha256:0000000000000000000000000000000000000000000000000000000000000000"

  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_secretsmanager_secret" "this" {
  for_each = local.backend_secret_keys

  name                    = "${var.project_name}/${var.environment}/${lower(replace(each.value, "_", "-"))}"
  recovery_window_in_days = var.environment == "production" ? 7 : 0
}

resource "aws_secretsmanager_secret_version" "this" {
  for_each = local.backend_secret_keys

  secret_id     = aws_secretsmanager_secret.this[each.value].id
  secret_string = local.backend_secret_values[each.value]
}

resource "aws_iam_role_policy" "secrets_access" {
  name = "${local.name_prefix}-secrets-access"
  role = data.aws_ssm_parameter.task_execution_role_name.value

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["secretsmanager:GetSecretValue"]
        Resource = [for s in aws_secretsmanager_secret.this : s.arn]
      }
    ]
  })
}

resource "aws_iam_role_policy" "s3_access" {
  name = "${var.project_name}-${var.environment}-ecs-task-s3-access"
  role = data.aws_ssm_parameter.task_role_name.value

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:PutObject", "s3:GetObject", "s3:HeadObject", "s3:DeleteObject"]
        Resource = "${data.aws_ssm_parameter.s3_bucket_arn.value}/*"
      },
      {
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = data.aws_ssm_parameter.s3_bucket_arn.value
      }
    ]
  })
}

module "service" {
  source = "./modules/ecs-service"

  name_prefix       = local.name_prefix
  container_name    = "backend"
  container_port    = 8080
  health_check_path = "/health"
  image             = aws_ssm_parameter.image.value
  cpu               = var.backend_cpu
  memory            = var.backend_memory
  desired_count     = var.desired_count
  max_count         = var.max_count

  cluster_arn             = data.aws_ssm_parameter.cluster_arn.value
  task_execution_role_arn = data.aws_ssm_parameter.task_execution_role_arn.value
  task_role_arn           = data.aws_ssm_parameter.task_role_arn.value
  private_subnet_ids      = split(",", data.aws_ssm_parameter.private_subnet_ids.value)
  ecs_security_group_id   = data.aws_ssm_parameter.ecs_security_group_id.value
  target_group_arn        = data.aws_ssm_parameter.target_group_arn_backend.value

  env_vars = merge({
    APP_ENV                  = var.environment
    PORT                     = "8080"
    REDIS_URL                = data.aws_ssm_parameter.redis_url.value
    API_PUBLIC_URL           = var.api_public_url
    APP_PUBLIC_URL           = var.app_public_url
    AWS_REGION               = var.aws_region
    AWS_S3_BUCKET            = data.aws_ssm_parameter.s3_bucket_name.value
    AWS_S3_PUBLIC_BASE_URL   = "https://${data.aws_ssm_parameter.s3_bucket_name.value}.s3.${var.aws_region}.amazonaws.com"
    SENDGRID_FROM_EMAIL      = var.sendgrid_from_email
    SENDGRID_FROM_NAME       = var.sendgrid_from_name
    CELLULANT_BASE_URL       = var.cellulant_base_url
    CELLULANT_CHECKOUT_URL   = var.cellulant_checkout_url
    ADVANTA_BASE_URL         = var.advanta_base_url
    TURNSTILE_HOSTNAME       = var.turnstile_hostname
    VIDEOSDK_BASE_URL        = var.videosdk_base_url
    BETTERSTACK_INGEST_URL   = var.betterstack_ingest_url
    BETTERSTACK_SERVICE_NAME = "fundis-backend-${var.environment}"
    BETTERSTACK_MIN_LEVEL    = var.betterstack_min_level
  }, local.backend_secret_env_vars)

  secrets = { for k, s in aws_secretsmanager_secret.this : k => s.arn }

  tags = {
    Environment = var.environment
    Project     = var.project_name
    Service     = "backend"
  }
}
