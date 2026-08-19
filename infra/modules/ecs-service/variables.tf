variable "name_prefix" {
  description = "e.g. fundis-staging-backend"
  type        = string
}

variable "container_name" {
  type = string
}

variable "container_port" {
  type = number
}

variable "health_check_path" {
  type    = string
  default = "/health"
}

variable "health_check_start_period" {
  type    = number
  default = 60
}

variable "image" {
  description = "Full image reference, e.g. <repo_url>@sha256:<digest>"
  type        = string
}

variable "cpu" {
  type = number
}

variable "memory" {
  type = number
}

variable "desired_count" {
  type = number
}

variable "max_count" {
  type = number
}

variable "env_vars" {
  type    = map(string)
  default = {}
}

variable "secrets" {
  description = "map of container env var name => Secrets Manager secret ARN"
  type        = map(string)
  default     = {}
}

variable "cluster_arn" {
  type = string
}

variable "task_execution_role_arn" {
  type = string
}

variable "task_role_arn" {
  type = string
}

variable "private_subnet_ids" {
  type = list(string)
}

variable "ecs_security_group_id" {
  type = string
}

variable "target_group_arn" {
  type = string
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "tags" {
  type    = map(string)
  default = {}
}
