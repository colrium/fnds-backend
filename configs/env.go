package configs

import (
	"os"
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

func GetPlunkKey() string {
	return os.Getenv("USE_PLUNK")
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

func FlutterwaveSecretKey() string {
	return os.Getenv("FLUTTERWAVE_SECRET_KEY")
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
