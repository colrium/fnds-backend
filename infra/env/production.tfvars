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

unset_secrets = [
  "FLUTTERWAVE_SECRET_KEY",
  "FLUTTERWAVE_WEBHOOK_SECRET_HASH",
  "KOPOKOPO_API_KEY",
  "SLACK_WEBHOOK_URL",
]
