package notifications

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestFeedCacheEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.March, 15, 10, 11, 12, 123, time.UTC)
	feed := Feed{
		Notifications: []Notification{
			{
				ID:        "notification-1",
				UserID:    "user-1",
				Type:      TypeNewApplication,
				Title:     "New application",
				Message:   "An artisan applied",
				ActionURL: "/applications/1",
				Read:      false,
				CreatedAt: createdAt,
				Delivery: &Delivery{
					Channel: DeliveryChannelEmail,
					To:      "private@example.test",
					Subject: "delivery-only subject",
					Body:    "delivery-only body",
				},
			},
		},
		UnreadCount: 4,
	}

	payload, err := encodeFeedCacheEnvelope(feed)
	if err != nil {
		t.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
	}

	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(payload, &topLevel); err != nil {
		t.Fatalf("encoded payload is invalid JSON: %v", err)
	}
	if len(topLevel) != 2 || topLevel["version"] == nil || topLevel["feed"] == nil {
		t.Fatalf("encoded top-level fields = %v, want only version and feed", mapKeys(topLevel))
	}
	for _, forbidden := range [][]byte{
		[]byte(`"success"`),
		[]byte(`"data"`),
		[]byte(`"delivery"`),
		[]byte("private@example.test"),
		[]byte("delivery-only subject"),
		[]byte("delivery-only body"),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("encoded payload %q contains forbidden value %q", payload, forbidden)
		}
	}

	got, classification := decodeFeedCacheEnvelope(payload)
	if classification != feedCacheDecodeValid {
		t.Fatalf("decodeFeedCacheEnvelope() classification = %v, want valid", classification)
	}
	feed.Notifications[0].Delivery = nil
	if !reflect.DeepEqual(got, feed) {
		t.Fatalf("decodeFeedCacheEnvelope() = %#v, want public feed %#v", got, feed)
	}
}

func TestFeedCacheEnvelopeAcceptsEmptyFeeds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		feed Feed
	}{
		{name: "initialized empty collection", feed: Feed{Notifications: []Notification{}}},
		{name: "zero feed canonicalized to empty collection", feed: Feed{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			payload, err := encodeFeedCacheEnvelope(test.feed)
			if err != nil {
				t.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
			}
			got, classification := decodeFeedCacheEnvelope(payload)
			if classification != feedCacheDecodeValid {
				t.Fatalf("decodeFeedCacheEnvelope() classification = %v, want valid", classification)
			}
			if got.Notifications == nil || len(got.Notifications) != 0 || got.UnreadCount != 0 {
				t.Fatalf("decodeFeedCacheEnvelope() = %#v, want initialized empty feed", got)
			}
		})
	}
}

func TestDecodeFeedCacheEnvelopeRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty payload"},
		{name: "malformed JSON", payload: []byte(`{"version":`)},
		{name: "null envelope", payload: []byte(`null`)},
		{name: "missing version", payload: []byte(`{"feed":{"notifications":[],"unread_count":0}}`)},
		{name: "null version", payload: []byte(`{"version":null,"feed":{"notifications":[],"unread_count":0}}`)},
		{name: "unsupported version", payload: []byte(`{"version":2,"feed":{"notifications":[],"unread_count":0}}`)},
		{name: "missing feed", payload: []byte(`{"version":1}`)},
		{name: "null feed", payload: []byte(`{"version":1,"feed":null}`)},
		{name: "non-object feed", payload: []byte(`{"version":1,"feed":[]}`)},
		{name: "missing notifications", payload: []byte(`{"version":1,"feed":{"unread_count":0}}`)},
		{name: "null notifications", payload: []byte(`{"version":1,"feed":{"notifications":null,"unread_count":0}}`)},
		{name: "wrong notifications type", payload: []byte(`{"version":1,"feed":{"notifications":{},"unread_count":0}}`)},
		{name: "missing unread count", payload: []byte(`{"version":1,"feed":{"notifications":[]}}`)},
		{name: "null unread count", payload: []byte(`{"version":1,"feed":{"notifications":[],"unread_count":null}}`)},
		{name: "wrong unread count type", payload: []byte(`{"version":1,"feed":{"notifications":[],"unread_count":"0"}}`)},
		{name: "negative unread count", payload: []byte(`{"version":1,"feed":{"notifications":[],"unread_count":-1}}`)},
		{name: "invalid notification field", payload: []byte(`{"version":1,"feed":{"notifications":[{"read":"no"}],"unread_count":0}}`)},
		{name: "arbitrary binary", payload: []byte{0xff, 0x00, 0x7f}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, classification := decodeFeedCacheEnvelope(test.payload)
			if classification != feedCacheDecodeInvalid {
				t.Fatalf("decodeFeedCacheEnvelope() classification = %v, want invalid miss", classification)
			}
			if !reflect.DeepEqual(got, Feed{}) {
				t.Fatalf("decodeFeedCacheEnvelope() feed = %#v, want zero feed", got)
			}
		})
	}
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
