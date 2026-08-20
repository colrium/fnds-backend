environment    = "production"
backend_cpu    = 512
backend_memory = 1024
desired_count  = 2
max_count      = 4

api_public_url         = "https://api.fundis.co.ke"
app_public_url         = "https://app.fundis.co.ke"
sendgrid_from_email    = "notifications@fundis.co.ke"
sendgrid_from_name     = "Fundis App"
cellulant_base_url     = "https://api-test.tingg.africa"
cellulant_checkout_url = "https://online.uat.tingg.africa/testing/express/checkout"
advanta_base_url       = "https://quicksms.advantasms.com"
turnstile_hostname     = ""
betterstack_ingest_url = "https://s2652431.eu-central-1a.betterstackdata.com"
betterstack_min_level  = "info"

stytch_env                   = "live"
stytch_employer_auth_enabled = "false"
# Must stay "true" while stytch_employer_auth_enabled is "false": with it unset
# the app blocks legacy login for any employer already holding an account_link.
legacy_employer_auth_enabled = "true"

# Retention purge stays in report-only mode until a data-retention policy is set.
workspace_retention_purge_enabled = "false"

unset_secrets = [
  "FLUTTERWAVE_SECRET_KEY",
  "FLUTTERWAVE_WEBHOOK_SECRET_HASH",
  "KOPOKOPO_API_KEY",
  "SLACK_WEBHOOK_URL",
  "STYTCH_PROJECT_ID",
  "STYTCH_SECRET",
  "STYTCH_WEBHOOK_SECRET",
  "ZOHO_MEETING_CLIENT_ID",
  "ZOHO_MEETING_CLIENT_SECRET",
  "ZOHO_MEETING_REFRESH_TOKEN",
  "ZOHO_MEETING_ZSOID",
  "ZOHO_MEETING_PRESENTER_ID",
]
