package configs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

// BedrockModelID returns the Amazon Bedrock model ID (or inference profile
// ID/ARN) used for CV parsing. Current-generation Claude models on Bedrock
// require an inference profile ID rather than a bare model ID — invoking one
// by bare model ID fails with ValidationException: "The provided model
// identifier is invalid."
//
// The fallback below uses a "global." inference profile rather than a
// region-prefixed one ("eu.", "us.") deliberately: a region-prefixed
// profile only resolves correctly when the Bedrock client's region matches
// the profile's prefix, and that region is resolved from AWS_REGION — an
// env var the AWS CLI/SDK's own config files (~/.aws/config) commonly also
// export into the process environment, which godotenv (see main.go) will
// never override. A shell with AWS_REGION already exported to some other
// region than the one in .env silently wins, and a region-prefixed profile
// then fails region validation with no indication *why* in the error. The
// "global." profile has no such region coupling, so this default keeps
// working regardless of which region actually gets resolved.
// Region is resolved the same way as the S3 client (see files.NewS3Store).
// Auth uses the AWS_BEARER_TOKEN_BEDROCK env var (a Bedrock API key) when
// set; the AWS SDK picks this up automatically and skips the IAM credential
// chain entirely, so a Bedrock-only bearer token works without granting
// AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY any Bedrock permissions.
func BedrockModelID() string {
	value := os.Getenv("BEDROCK_MODEL_ID")
	if value == "" {
		return "global.anthropic.claude-haiku-4-5-20251001-v1:0"
	}
	return value
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

// Zoho Meeting configuration for oral assessment video calls.
func ZohoMeetingClientID() string     { return os.Getenv("ZOHO_MEETING_CLIENT_ID") }
func ZohoMeetingClientSecret() string { return os.Getenv("ZOHO_MEETING_CLIENT_SECRET") }
func ZohoMeetingRefreshToken() string { return os.Getenv("ZOHO_MEETING_REFRESH_TOKEN") }
func ZohoMeetingZSOID() string        { return os.Getenv("ZOHO_MEETING_ZSOID") }

// AssessmentRescheduleCutoff is how close to an appointment an artisan may still
// cancel in order to rebook. Inside the window the assessor has committed the
// slot. Configurable because it is a business rule, not a technical constant.
func AssessmentRescheduleCutoff() time.Duration {
	const fallback = 12 * time.Hour
	raw := os.Getenv("ASSESSMENT_RESCHEDULE_CUTOFF")
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	// A negative or zero cutoff would let an artisan cancel a session already in
	// progress, so treat it as misconfiguration and keep the default.
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// ZohoAccountsBaseURL is the OAuth host for the Zoho datacenter holding the
// account. Zoho does not replicate accounts across datacenters, so an org
// created in the EU or India cannot authenticate against the default .com host.
func ZohoAccountsBaseURL() string {
	if v := os.Getenv("ZOHO_ACCOUNTS_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://accounts.zoho.com"
}

// ZohoMeetingBaseURL is the Meeting API host, which must match the same
// datacenter as ZohoAccountsBaseURL.
func ZohoMeetingBaseURL() string {
	if v := os.Getenv("ZOHO_MEETING_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://meeting.zoho.com"
}

// ZohoMeetingTimezone is the IANA zone Zoho should interpret meeting start
// times in. It must be a zone name, not an offset, so DST is handled by the
// tzdata rules rather than a fixed guess.
func ZohoMeetingTimezone() string {
	if v := os.Getenv("ZOHO_MEETING_TIMEZONE"); v != "" {
		return v
	}
	return "Africa/Nairobi"
}

// ZohoMeetingPresenterID returns the Zoho user ID of the meeting host.
// Returns 0 if not configured (the service will fail at meeting creation time).
func ZohoMeetingPresenterID() int64 {
	v := os.Getenv("ZOHO_MEETING_PRESENTER_ID")
	if v == "" {
		return 0
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// BedrockRegion is the AWS region used for AI scoring via Bedrock.
func BedrockRegion() string { return os.Getenv("BEDROCK_REGION") }

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
