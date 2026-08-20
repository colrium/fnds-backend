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

variable "stytch_public_token" {
  description = "Safe to expose to the frontend; used by the Stytch JS SDK."
  type        = string
  default     = ""
}

variable "stytch_env" {
  description = "\"test\" or \"live\"."
  type        = string
  default     = "test"
}

variable "stytch_employer_auth_enabled" {
  description = "Master switch for employer Stytch authentication."
  type        = string
  default     = "false"
}

# The app treats an unset value as false, which would reject every employer who
# still holds a legacy password credential. Default to true so the rollback path
# (Req 21.12: legacy login keeps working for at least 60 days after cutover)
# survives an omitted tfvar. Set to false only once migration is verified.
variable "legacy_employer_auth_enabled" {
  description = "Keeps POST /api/v1/login working for employers not yet migrated to Stytch."
  type        = string
  default     = "true"
}

# Workspace data-retention purge. Disabled by default: with purge off the job
# only reports what it WOULD delete. The ages below are floors set by
# requirements (Req 12.10, Req 23.6) — the app fails startup on a smaller value.
variable "workspace_retention_purge_enabled" {
  description = "Whether the retention job actually deletes, or only reports."
  type        = string
  default     = "false"
}

variable "workspace_retention_max_invitation_age" {
  description = "Age at which a non-pending invitation becomes purgeable. Minimum 4320h."
  type        = string
  default     = "8760h"
}

variable "workspace_retention_max_audit_age" {
  description = "Age at which an audit log entry becomes purgeable. Minimum 17520h."
  type        = string
  default     = "26280h"
}

variable "zoho_accounts_base_url" {
  description = "Zoho datacenter host override. Empty uses the app's .com default."
  type        = string
  default     = ""
}

variable "zoho_meeting_base_url" {
  description = "Zoho datacenter host override. Empty uses the app's .com default."
  type        = string
  default     = ""
}

variable "zoho_meeting_timezone" {
  description = "IANA zone Zoho interprets meeting start times in. Empty uses the app's Africa/Nairobi default."
  type        = string
  default     = ""
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

variable "stytch_project_id" {
  type      = string
  sensitive = true
  default   = ""
}

variable "stytch_secret" {
  type      = string
  sensitive = true
  default   = ""
}

variable "stytch_webhook_secret" {
  type      = string
  sensitive = true
  default   = ""
}

variable "zoho_meeting_client_id" {
  type      = string
  sensitive = true
  default   = ""
}

variable "zoho_meeting_client_secret" {
  type      = string
  sensitive = true
  default   = ""
}

variable "zoho_meeting_refresh_token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "zoho_meeting_zsoid" {
  type      = string
  sensitive = true
  default   = ""
}

variable "zoho_meeting_presenter_id" {
  type      = string
  sensitive = true
  default   = ""
}

variable "unset_secrets" {
  description = "Keys deliberately left unset in this env. Blank and not listed here fails the plan."
  type        = set(string)
  default     = []
}
