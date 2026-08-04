package configs

import (
	"fmt"
	"os"
	"strconv"
	"time"
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

// SlackOpsNotifsURL is the dedicated Slack Incoming Webhook for operations
// activity notifications.
func SlackOpsNotifsURL() string {
	return os.Getenv("SLACK_OPS_NOTIFS_URL")
}

// SlackWebhookURL is the general-purpose Slack Incoming Webhook, used as a
// fallback when a channel-specific URL is not configured.
func SlackWebhookURL() string {
	return os.Getenv("SLACK_WEBHOOK_URL")
}

// Better Stack (Logtail) error log forwarding. These accessors deliberately
// return the raw environment value rather than a parsed one: all parsing,
// trimming, and range clamping lives in logging.ResolveConfig so that every
// rejection path is unit-testable without t.Setenv and validation is not split
// across two packages.
func BetterStackSourceToken() string  { return os.Getenv("BETTERSTACK_SOURCE_TOKEN") }
func BetterStackIngestURL() string    { return os.Getenv("BETTERSTACK_INGEST_URL") }
func BetterStackServiceName() string  { return os.Getenv("BETTERSTACK_SERVICE_NAME") }
func BetterStackMinLevel() string     { return os.Getenv("BETTERSTACK_MIN_LEVEL") }
func BetterStackBatchSize() string    { return os.Getenv("BETTERSTACK_BATCH_SIZE") }
func BetterStackFlushSeconds() string { return os.Getenv("BETTERSTACK_FLUSH_SECONDS") }
func BetterStackQueueSize() string    { return os.Getenv("BETTERSTACK_QUEUE_SIZE") }

const (
	notificationCacheEnabledDefault = true
	notificationCacheTTLDefault     = 30 * time.Second
	notificationCacheTTLMin         = 5 * time.Second
	notificationCacheTTLMax         = 5 * time.Minute

	notificationCacheOpTimeoutDefault = 100 * time.Millisecond
	notificationCacheOpTimeoutMin     = 10 * time.Millisecond
	notificationCacheOpTimeoutMax     = 1 * time.Second
)

// NotificationCacheSettings is the immutable, validated configuration for the
// Redis-backed notifications feed cache. Zero-value TTL/OperationTimeout only
// occur when Enabled is false.
type NotificationCacheSettings struct {
	Enabled          bool
	TTL              time.Duration
	OperationTimeout time.Duration
}

// LoadNotificationCacheSettings parses NOTIFICATIONS_CACHE_ENABLED,
// NOTIFICATIONS_CACHE_TTL, and NOTIFICATIONS_CACHE_OP_TIMEOUT. Invalid values
// return a descriptive error instead of clamping to a default, so operator
// misconfiguration fails startup rather than silently changing behavior.
func LoadNotificationCacheSettings() (NotificationCacheSettings, error) {
	enabled, err := parseBoolEnv("NOTIFICATIONS_CACHE_ENABLED", notificationCacheEnabledDefault)
	if err != nil {
		return NotificationCacheSettings{}, err
	}

	ttl, err := parseDurationEnvInRange(
		"NOTIFICATIONS_CACHE_TTL",
		notificationCacheTTLDefault,
		notificationCacheTTLMin,
		notificationCacheTTLMax,
	)
	if err != nil {
		return NotificationCacheSettings{}, err
	}

	opTimeout, err := parseDurationEnvInRange(
		"NOTIFICATIONS_CACHE_OP_TIMEOUT",
		notificationCacheOpTimeoutDefault,
		notificationCacheOpTimeoutMin,
		notificationCacheOpTimeoutMax,
	)
	if err != nil {
		return NotificationCacheSettings{}, err
	}

	return NotificationCacheSettings{
		Enabled:          enabled,
		TTL:              ttl,
		OperationTimeout: opTimeout,
	}, nil
}

func parseBoolEnv(name string, defaultValue bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parsing %s=%q: %w", name, value, err)
	}
	return parsed, nil
}

func parseDurationEnvInRange(
	name string,
	defaultValue time.Duration,
	minValue time.Duration,
	maxValue time.Duration,
) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q: %w", name, value, err)
	}
	if parsed < minValue || parsed > maxValue {
		return 0, fmt.Errorf(
			"%s=%s is out of range [%s, %s]",
			name, parsed, minValue, maxValue,
		)
	}
	return parsed, nil
}
