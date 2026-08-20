variable "environment" {
  type = string
  validation {
    condition     = contains(["staging", "production"], var.environment)
    error_message = "environment must be staging or production."
  }
}

variable "aws_region" {
  type    = string
  default = "af-south-1"
}

variable "project_name" {
  type    = string
  default = "fundis"
}

variable "backend_cpu" {
  type = number
}

variable "backend_memory" {
  type = number
}

variable "desired_count" {
  type = number
}

variable "max_count" {
  type = number
}

variable "api_public_url" {
  type = string
}

variable "app_public_url" {
  type = string
}

variable "sendgrid_from_email" {
  type = string
}

variable "sendgrid_from_name" {
  type = string
}

variable "cellulant_base_url" {
  type = string
}

variable "cellulant_checkout_url" {
  type = string
}

variable "advanta_base_url" {
  type = string
}

variable "turnstile_hostname" {
  type = string
}

variable "betterstack_ingest_url" {
  type = string
}

variable "betterstack_min_level" {
  type    = string
  default = "info"
}

variable "mongodb_uri_override" {
  description = "Set to use an external Mongo (e.g. Atlas) instead of the platform's self-hosted instance. Empty string means use the platform value."
  type        = string
  default     = ""
  sensitive   = true
}

# --- Sensitive: supplied as TF_VAR_* GitHub Environment secrets in CI ---

variable "session_key" {
  type      = string
  sensitive = true
}

variable "sendgrid_api_key" {
  type      = string
  sensitive = true
}

variable "flutterwave_secret_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "flutterwave_webhook_secret_hash" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_api_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_access_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_secret_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_iv_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_client_id" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_client_secret" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cellulant_service_code" {
  type      = string
  sensitive = true
  default   = ""
}

variable "kopokopo_api_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "advanta_api_key" {
  type      = string
  sensitive = true
}

variable "advanta_partner_id" {
  type      = string
  sensitive = true
}

variable "advanta_shortcode" {
  type      = string
  sensitive = true
}

variable "turnstile_secret_key" {
  type      = string
  sensitive = true
  default   = ""
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
  default   = ""
}

variable "slack_ops_notifs_url" {
  type      = string
  sensitive = true
  default   = ""
}

variable "betterstack_source_token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "unset_secrets" {
  description = "Keys deliberately left unset in this env. Blank and not listed here fails the plan."
  type        = set(string)
  default     = []
}
