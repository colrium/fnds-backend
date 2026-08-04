package notifications

import (
	"errors"
	"strings"
	"testing"
)

func TestUserToken(t *testing.T) {
	t.Parallel()

	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

	got, err := userToken("abc")
	if err != nil {
		t.Fatalf("userToken() error = %v", err)
	}
	if got != want {
		t.Fatalf("userToken() = %q, want %q", got, want)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("userToken() = %q, want lowercase hexadecimal", got)
	}
}

func TestUserTokenRejectsEmptyUserID(t *testing.T) {
	t.Parallel()

	token, err := userToken("")
	if !errors.Is(err, errEmptyCacheUserID) {
		t.Fatalf("userToken() error = %v, want %v", err, errEmptyCacheUserID)
	}
	if token != "" {
		t.Fatalf("userToken() = %q, want empty token on error", token)
	}
}

func TestGenerationKey(t *testing.T) {
	t.Parallel()

	const (
		appEnvironment = "staging"
		userID         = "user:private@example.test"
	)
	token, err := userToken(userID)
	if err != nil {
		t.Fatalf("userToken() error = %v", err)
	}
	want := "fundis:staging:notifications:feed:v1:u:" + token + ":generation"

	got, err := generationKey(appEnvironment, userID)
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	if got != want {
		t.Fatalf("generationKey() = %q, want %q", got, want)
	}
	if strings.Contains(got, userID) {
		t.Fatalf("generationKey() exposed raw user ID %q", userID)
	}
}

func TestFeedKey(t *testing.T) {
	t.Parallel()

	const (
		appEnvironment  = "production"
		userID          = "authenticated-user-42"
		generation      = uint64(17)
		normalizedLimit = int64(30)
	)

	got, err := feedKey(appEnvironment, userID, generation, normalizedLimit)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}

	token, err := userToken(userID)
	if err != nil {
		t.Fatalf("userToken() error = %v", err)
	}
	want := "fundis:production:notifications:feed:v1:u:" + token + ":g:17:limit:30"
	if got != want {
		t.Fatalf("feedKey() = %q, want %q", got, want)
	}
	if strings.Contains(got, userID) {
		t.Fatalf("feedKey() exposed raw user ID %q", userID)
	}
}

func TestFeedKeyDeterminismAndVariantSeparation(t *testing.T) {
	t.Parallel()

	base, err := feedKey("test", "user-a", 4, 30)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}
	repeated, err := feedKey("test", "user-a", 4, 30)
	if err != nil {
		t.Fatalf("feedKey() repeated error = %v", err)
	}
	if repeated != base {
		t.Fatalf("feedKey() repeated = %q, want deterministic key %q", repeated, base)
	}

	tests := []struct {
		name       string
		userID     string
		generation uint64
		limit      int64
	}{
		{name: "different user", userID: "user-b", generation: 4, limit: 30},
		{name: "different generation", userID: "user-a", generation: 5, limit: 30},
		{name: "different normalized limit", userID: "user-a", generation: 4, limit: 31},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, keyErr := feedKey("test", test.userID, test.generation, test.limit)
			if keyErr != nil {
				t.Fatalf("feedKey() error = %v", keyErr)
			}
			if got == base {
				t.Fatalf("feedKey() = %q, want key distinct from %q", got, base)
			}
		})
	}
}

func TestCacheKeysRejectInvalidInternalInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userID    string
		limit     int64
		wantError error
	}{
		{name: "empty user", userID: "", limit: 30, wantError: errEmptyCacheUserID},
		{name: "zero limit", userID: "user-a", limit: 0, wantError: errInvalidCacheLimit},
		{name: "negative limit", userID: "user-a", limit: -1, wantError: errInvalidCacheLimit},
		{name: "limit above maximum", userID: "user-a", limit: 101, wantError: errInvalidCacheLimit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			key, err := feedKey("test", test.userID, 0, test.limit)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("feedKey() error = %v, want %v", err, test.wantError)
			}
			if key != "" {
				t.Fatalf("feedKey() = %q, want empty key on error", key)
			}
		})
	}

	key, err := generationKey("test", "")
	if !errors.Is(err, errEmptyCacheUserID) {
		t.Fatalf("generationKey() error = %v, want %v", err, errEmptyCacheUserID)
	}
	if key != "" {
		t.Fatalf("generationKey() = %q, want empty key on error", key)
	}
}

func TestFeedKeyAcceptsLimitBounds(t *testing.T) {
	t.Parallel()

	for _, limit := range []int64{1, 100} {
		limit := limit
		t.Run(string(rune(limit)), func(t *testing.T) {
			t.Parallel()

			if _, err := feedKey("test", "user-a", 0, limit); err != nil {
				t.Fatalf("feedKey() limit %d error = %v", limit, err)
			}
		})
	}
}
