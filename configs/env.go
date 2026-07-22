package configs

import (
	"os"
	"strconv"
)

func AppEnv() string {
	return os.Getenv("APP_ENV")
}

func EnvIsProd() bool {
	return AppEnv() == "production"
}

func GetRedisUrl() string {
	return os.Getenv("REDIS_URL")
}

func GetSessionKey() string {
	return os.Getenv("SESSION_KEY")
}

func EnvMongoURI() string {
	return os.Getenv("MONGODB_URI")
}

func EnvPort() string {
	return os.Getenv("PORT")
}

func SendGridAPIKey() string {
	return os.Getenv("SENDGRID_API_KEY")
}

func SendGridFromEmail() string {
	return os.Getenv("SENDGRID_FROM_EMAIL")
}

func SendGridFromName() string {
	value := os.Getenv("SENDGRID_FROM_NAME")
	if value == "" {
		return "Fundis"
	}
	return value
}

func AdvantaApiKey() string {
	return os.Getenv("ADVANTA_API_KEY")
}

func AdvantaPartnerID() string {
	return os.Getenv("ADVANTA_PARTNER_ID")
}

func AdvantaShortcode() string {
	return os.Getenv("ADVANTA_SHORTCODE")
}

func TurnstileSecretKey() string {
	return os.Getenv("TURNSTILE_SECRET_KEY")
}

func TurnstileHostname() string {
	return os.Getenv("TURNSTILE_HOSTNAME")
}

func FlutterwaveSecretKey() string {
	return os.Getenv("FLUTTERWAVE_SECRET_KEY")
}

func FlutterwaveWebhookSecretHash() string {
	return os.Getenv("FLUTTERWAVE_WEBHOOK_SECRET_HASH")
}

func CellulantAccessKey() string {
	return os.Getenv("CELLULANT_ACCESS_KEY")
}

func CellulantSecretKey() string {
	return os.Getenv("CELLULANT_SECRET_KEY")
}

func CellulantIVKey() string {
	return os.Getenv("CELLULANT_IV_KEY")
}

func CellulantServiceCode() string {
	return os.Getenv("CELLULANT_SERVICE_CODE")
}

func CellulantCheckoutURL() string {
	return os.Getenv("CELLULANT_CHECKOUT_URL")
}

func CellulantBaseURL() string {
	return os.Getenv("CELLULANT_BASE_URL")
}

func CellulantClientID() string {
	return os.Getenv("CELLULANT_CLIENT_ID")
}

func CellulantClientSecret() string {
	return os.Getenv("CELLULANT_CLIENT_SECRET")
}

func CellulantAPIKey() string {
	return os.Getenv("CELLULANT_API_KEY")
}

func AWSRegion() string {
	return os.Getenv("AWS_REGION")
}

func AWSS3Bucket() string {
	return os.Getenv("AWS_S3_BUCKET")
}

func AWSS3PublicBaseURL() string {
	return os.Getenv("AWS_S3_PUBLIC_BASE_URL")
}

func AWSS3MaxUploadBytes() (int64, error) {
	value := os.Getenv("AWS_S3_MAX_UPLOAD_BYTES")
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

// APIPublicURL is the public base URL of this backend, used to build links
// (e.g. quotation accept/reject) that customers open from email/SMS.
func APIPublicURL() string {
	return os.Getenv("API_PUBLIC_URL")
}

// AppPublicURL is the public base URL of the customer-facing frontend, used as
// the redirect target after a customer acts on a link.
func AppPublicURL() string {
	return os.Getenv("APP_PUBLIC_URL")
}
