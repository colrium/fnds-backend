package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

var (
	errEmptyCacheUserID  = errors.New("notifications: cache user id is required")
	errInvalidCacheLimit = errors.New(
		"notifications: normalized cache limit must be between 1 and 100",
	)
)

// userToken creates the opaque per-user namespace used in notification cache
// keys. Callers must pass the authenticated user ID, never a request-supplied
// identifier.
func userToken(userID string) (string, error) {
	if userID == "" {
		return "", errEmptyCacheUserID
	}

	digest := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(digest[:]), nil
}

func generationKey(appEnvironment string, userID string) (string, error) {
	token, err := userToken(userID)
	if err != nil {
		return "", err
	}

	return cacheUserKeyPrefix(appEnvironment, token) + ":generation", nil
}

func feedKey(
	appEnvironment string,
	userID string,
	generation uint64,
	normalizedLimit int64,
) (string, error) {
	if normalizedLimit < 1 || normalizedLimit > 100 {
		return "", fmt.Errorf("%w: got %d", errInvalidCacheLimit, normalizedLimit)
	}

	token, err := userToken(userID)
	if err != nil {
		return "", err
	}

	return cacheUserKeyPrefix(appEnvironment, token) +
		":g:" + strconv.FormatUint(generation, 10) +
		":limit:" + strconv.FormatInt(normalizedLimit, 10), nil
}

func cacheUserKeyPrefix(appEnvironment string, token string) string {
	return feedCacheKeyRoot + ":" + appEnvironment +
		":" + feedCacheKeyDomain +
		":v" + strconv.Itoa(feedCacheKeyLayoutVersion) +
		":u:" + token
}
