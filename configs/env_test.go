package configs

import (
	"strings"
	"testing"
	"time"
)

func TestAppEnv(t *testing.T) {
	t.Setenv("APP_ENV", "test")

	if got := AppEnv(); got != "test" {
		t.Errorf("AppEnv() = %q, want %q", got, "test")
	}
}

func TestEnvIsProd(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"production", "production", true},
		{"development", "development", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tt.env)

			if got := EnvIsProd(); got != tt.want {
				t.Errorf("EnvIsProd() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvPort(t *testing.T) {
	t.Setenv("PORT", "8080")

	if got := EnvPort(); got != "8080" {
		t.Errorf("EnvPort() = %q, want %q", got, "8080")
	}
}

func TestEnvMongoURI(t *testing.T) {
	t.Setenv("MONGODB_URI", "mongodb://localhost:27017")

	if got := EnvMongoURI(); got != "mongodb://localhost:27017" {
		t.Errorf("EnvMongoURI() = %q, want %q", got, "mongodb://localhost:27017")
	}
}

func TestGetSessionKey(t *testing.T) {
	t.Setenv("SESSION_KEY", "secret-key")

	if got := GetSessionKey(); got != "secret-key" {
		t.Errorf("GetSessionKey() = %q, want %q", got, "secret-key")
	}
}

func TestTurnstileSecretKey(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret-123")

	if got := TurnstileSecretKey(); got != "secret-123" {
		t.Errorf("TurnstileSecretKey() = %q, want %q", got, "secret-123")
	}
}

func TestTurnstileHostname(t *testing.T) {
	t.Setenv("TURNSTILE_HOSTNAME", "app.example.com")

	if got := TurnstileHostname(); got != "app.example.com" {
		t.Errorf("TurnstileHostname() = %q, want %q", got, "app.example.com")
	}
}

func TestEnvVarsReturnEmptyWhenUnset(t *testing.T) {
	vars := []struct {
		name string
		fn   func() string
	}{
		{"AppEnv", AppEnv},
		{"GetRedisUrl", GetRedisUrl},
		{"GetSessionKey", GetSessionKey},
		{"EnvMongoURI", EnvMongoURI},
		{"EnvPort", EnvPort},
	}

	for _, v := range vars {
		t.Run(v.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "")
			t.Setenv("REDIS_URL", "")
			t.Setenv("SESSION_KEY", "")
			t.Setenv("MONGODB_URI", "")
			t.Setenv("PORT", "")

			if got := v.fn(); got != "" {
				t.Errorf("%s() = %q, want empty string when env var is unset", v.name, got)
			}
		})
	}
}

func TestLoadNotificationCacheSettingsDefaults(t *testing.T) {
	t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "")
	t.Setenv("NOTIFICATIONS_CACHE_TTL", "")
	t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", "")

	got, err := LoadNotificationCacheSettings()
	if err != nil {
		t.Fatalf("LoadNotificationCacheSettings() error = %v", err)
	}
	want := NotificationCacheSettings{
		Enabled:          true,
		TTL:              30 * time.Second,
		OperationTimeout: 100 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("LoadNotificationCacheSettings() = %+v, want %+v", got, want)
	}
}

func TestLoadNotificationCacheSettingsValidOverrides(t *testing.T) {
	t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "false")
	t.Setenv("NOTIFICATIONS_CACHE_TTL", "1m")
	t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", "250ms")

	got, err := LoadNotificationCacheSettings()
	if err != nil {
		t.Fatalf("LoadNotificationCacheSettings() error = %v", err)
	}
	want := NotificationCacheSettings{
		Enabled:          false,
		TTL:              time.Minute,
		OperationTimeout: 250 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("LoadNotificationCacheSettings() = %+v, want %+v", got, want)
	}
}

func TestLoadNotificationCacheSettingsExactBounds(t *testing.T) {
	tests := []struct {
		name    string
		ttl     string
		timeout string
		want    NotificationCacheSettings
	}{
		{
			name:    "lower bounds",
			ttl:     "5s",
			timeout: "10ms",
			want: NotificationCacheSettings{
				Enabled:          true,
				TTL:              5 * time.Second,
				OperationTimeout: 10 * time.Millisecond,
			},
		},
		{
			name:    "upper bounds",
			ttl:     "5m",
			timeout: "1s",
			want: NotificationCacheSettings{
				Enabled:          true,
				TTL:              5 * time.Minute,
				OperationTimeout: time.Second,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "true")
			t.Setenv("NOTIFICATIONS_CACHE_TTL", test.ttl)
			t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", test.timeout)

			got, err := LoadNotificationCacheSettings()
			if err != nil {
				t.Fatalf("LoadNotificationCacheSettings() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("LoadNotificationCacheSettings() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLoadNotificationCacheSettingsInvalidBoolean(t *testing.T) {
	t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "not-a-bool")
	t.Setenv("NOTIFICATIONS_CACHE_TTL", "")
	t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", "")

	_, err := LoadNotificationCacheSettings()
	if err == nil {
		t.Fatal("LoadNotificationCacheSettings() error = nil, want invalid boolean error")
	}
	if !strings.Contains(err.Error(), "NOTIFICATIONS_CACHE_ENABLED") {
		t.Errorf("LoadNotificationCacheSettings() error = %q, want NOTIFICATIONS_CACHE_ENABLED context", err)
	}
}

func TestLoadNotificationCacheSettingsMalformedDurations(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		value   string
		wantSub string
	}{
		{
			name:    "malformed ttl",
			envVar:  "NOTIFICATIONS_CACHE_TTL",
			value:   "thirty-seconds",
			wantSub: "NOTIFICATIONS_CACHE_TTL",
		},
		{
			name:    "malformed op timeout",
			envVar:  "NOTIFICATIONS_CACHE_OP_TIMEOUT",
			value:   "fast",
			wantSub: "NOTIFICATIONS_CACHE_OP_TIMEOUT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "")
			t.Setenv("NOTIFICATIONS_CACHE_TTL", "")
			t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", "")
			t.Setenv(test.envVar, test.value)

			_, err := LoadNotificationCacheSettings()
			if err == nil {
				t.Fatalf("LoadNotificationCacheSettings() error = nil, want malformed duration error")
			}
			if !strings.Contains(err.Error(), test.wantSub) {
				t.Errorf("LoadNotificationCacheSettings() error = %q, want %q context", err, test.wantSub)
			}
		})
	}
}

func TestLoadNotificationCacheSettingsOutOfRange(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		value   string
		wantSub string
	}{
		{name: "ttl below minimum", envVar: "NOTIFICATIONS_CACHE_TTL", value: "4s", wantSub: "NOTIFICATIONS_CACHE_TTL"},
		{name: "ttl above maximum", envVar: "NOTIFICATIONS_CACHE_TTL", value: "5m1s", wantSub: "NOTIFICATIONS_CACHE_TTL"},
		{
			name:    "op timeout below minimum",
			envVar:  "NOTIFICATIONS_CACHE_OP_TIMEOUT",
			value:   "9ms",
			wantSub: "NOTIFICATIONS_CACHE_OP_TIMEOUT",
		},
		{
			name:    "op timeout above maximum",
			envVar:  "NOTIFICATIONS_CACHE_OP_TIMEOUT",
			value:   "1s1ms",
			wantSub: "NOTIFICATIONS_CACHE_OP_TIMEOUT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NOTIFICATIONS_CACHE_ENABLED", "")
			t.Setenv("NOTIFICATIONS_CACHE_TTL", "")
			t.Setenv("NOTIFICATIONS_CACHE_OP_TIMEOUT", "")
			t.Setenv(test.envVar, test.value)

			_, err := LoadNotificationCacheSettings()
			if err == nil {
				t.Fatalf("LoadNotificationCacheSettings() error = nil, want out-of-range error")
			}
			if !strings.Contains(err.Error(), test.wantSub) {
				t.Errorf("LoadNotificationCacheSettings() error = %q, want %q context", err, test.wantSub)
			}
		})
	}
}
