environment    = "staging"
backend_cpu    = 256
backend_memory = 512
desired_count  = 1
max_count      = 2

api_public_url         = "https://staging-api.fundis.co.ke"
app_public_url         = "https://staging.fundis.co.ke"
sendgrid_from_email    = "notifications@fundis.co.ke"
sendgrid_from_name     = "Fundis"
cellulant_base_url     = ""
cellulant_checkout_url = "https://online.uat.tingg.africa/testing/express/checkout"
advanta_base_url       = "https://quicksms.advantasms.com"
turnstile_hostname     = ""
betterstack_ingest_url = ""

unset_secrets = [
  "SENDGRID_API_KEY",
  "FLUTTERWAVE_SECRET_KEY",
  "FLUTTERWAVE_WEBHOOK_SECRET_HASH",
  "CELLULANT_API_KEY",
  "CELLULANT_ACCESS_KEY",
  "CELLULANT_SECRET_KEY",
  "CELLULANT_IV_KEY",
  "CELLULANT_CLIENT_ID",
  "CELLULANT_CLIENT_SECRET",
  "CELLULANT_SERVICE_CODE",
  "KOPOKOPO_API_KEY",
  "TURNSTILE_SECRET_KEY",
  "SLACK_WEBHOOK_URL",
  "SLACK_OPS_NOTIFS_URL",
  "BETTERSTACK_SOURCE_TOKEN",
]
