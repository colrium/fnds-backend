package notifications

import (
	"bytes"
	"encoding/json"
)

// feedCacheEnvelope is the cache-only representation of a notifications feed.
// It deliberately contains neither the HTTP response envelope nor delivery
// state, which Notification excludes from JSON.
type feedCacheEnvelope struct {
	Version int  `json:"version"`
	Feed    Feed `json:"feed"`
}

type feedCacheDecodeClassification uint8

const (
	// feedCacheDecodeInvalid identifies an entry that must be handled as a
	// cache miss and may be deleted by the caller on a best-effort basis.
	feedCacheDecodeInvalid feedCacheDecodeClassification = iota
	feedCacheDecodeValid
)

func encodeFeedCacheEnvelope(feed Feed) ([]byte, error) {
	// MongoStore returns an initialized collection for an empty feed. Preserve
	// that public shape for callers that construct the zero Feed value too.
	if feed.Notifications == nil {
		feed.Notifications = []Notification{}
	}

	return json.Marshal(feedCacheEnvelope{
		Version: feedCacheSchemaVersion,
		Feed:    feed,
	})
}

func decodeFeedCacheEnvelope(payload []byte) (Feed, feedCacheDecodeClassification) {
	var envelope struct {
		Version *int            `json:"version"`
		Feed    json.RawMessage `json:"feed"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil ||
		envelope.Version == nil ||
		*envelope.Version != feedCacheSchemaVersion ||
		missingJSONValue(envelope.Feed) {
		return Feed{}, feedCacheDecodeInvalid
	}

	var encodedFeed struct {
		Notifications json.RawMessage `json:"notifications"`
		UnreadCount   json.RawMessage `json:"unread_count"`
	}
	if err := json.Unmarshal(envelope.Feed, &encodedFeed); err != nil ||
		missingJSONValue(encodedFeed.Notifications) ||
		missingJSONValue(encodedFeed.UnreadCount) {
		return Feed{}, feedCacheDecodeInvalid
	}

	var feed Feed
	if err := json.Unmarshal(encodedFeed.Notifications, &feed.Notifications); err != nil ||
		feed.Notifications == nil {
		return Feed{}, feedCacheDecodeInvalid
	}
	if err := json.Unmarshal(encodedFeed.UnreadCount, &feed.UnreadCount); err != nil ||
		feed.UnreadCount < 0 {
		return Feed{}, feedCacheDecodeInvalid
	}

	return feed, feedCacheDecodeValid
}

func missingJSONValue(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
