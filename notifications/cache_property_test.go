package notifications

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

// TestPropertyPerUserCacheIsolation is Property 1: for distinct user IDs, the
// generated feed keys differ and expose neither raw user ID.
func TestPropertyPerUserCacheIsolation(t *testing.T) {
	t.Parallel()

	property := func(seedA, seedB uint32, generation uint64, rawLimit int8) bool {
		userA := "user-" + strconv.FormatUint(uint64(seedA), 10)
		userB := "user-" + strconv.FormatUint(uint64(seedB), 10)
		if userA == userB {
			return true
		}
		limit := normalizeQuickLimit(rawLimit)

		keyA, err := feedKey("test", userA, generation, limit)
		if err != nil {
			t.Logf("feedKey(userA) error = %v", err)
			return false
		}
		keyB, err := feedKey("test", userB, generation, limit)
		if err != nil {
			t.Logf("feedKey(userB) error = %v", err)
			return false
		}
		if keyA == keyB {
			t.Logf("feedKey collision for distinct users %q and %q", userA, userB)
			return false
		}
		if strings.Contains(keyA, userA) || strings.Contains(keyB, userB) {
			t.Logf("feedKey exposed raw user ID: %q / %q", keyA, keyB)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatalf("Property 1 (per-user cache isolation) failed: %v", err)
	}
}

// TestPropertyQueryVariantCorrectness is Property 2: raw limits that
// normalize equally share a cache variant; distinct normalized limits never
// collide for the same user and generation.
func TestPropertyQueryVariantCorrectness(t *testing.T) {
	t.Parallel()

	property := func(userSeed uint32, generation uint64, rawLimitA, rawLimitB int16) bool {
		userID := "user-" + strconv.FormatUint(uint64(userSeed), 10)
		limitA := normalizeHandlerLimit(int64(rawLimitA))
		limitB := normalizeHandlerLimit(int64(rawLimitB))

		keyA, err := feedKey("test", userID, generation, limitA)
		if err != nil {
			t.Logf("feedKey(limitA) error = %v", err)
			return false
		}
		keyB, err := feedKey("test", userID, generation, limitB)
		if err != nil {
			t.Logf("feedKey(limitB) error = %v", err)
			return false
		}

		if limitA == limitB {
			return keyA == keyB
		}
		return keyA != keyB
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatalf("Property 2 (query variant correctness) failed: %v", err)
	}
}

// TestPropertyContractEquivalentSerialization is Property 3: any valid
// generated feed round-trips through the cache envelope without changing
// public fields, order, unread count, or delivery omission.
func TestPropertyContractEquivalentSerialization(t *testing.T) {
	t.Parallel()

	property := func(seed genFeed) bool {
		feed := seed.toFeed()

		payload, err := encodeFeedCacheEnvelope(feed)
		if err != nil {
			t.Logf("encodeFeedCacheEnvelope() error = %v", err)
			return false
		}

		got, classification := decodeFeedCacheEnvelope(payload)
		if classification != feedCacheDecodeValid {
			t.Logf("decodeFeedCacheEnvelope() classification = %v, want valid", classification)
			return false
		}

		want := feed
		for i := range want.Notifications {
			want.Notifications[i].Delivery = nil
		}
		if !reflect.DeepEqual(got, want) {
			t.Logf("round-trip mismatch: got %#v, want %#v", got, want)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatalf("Property 3 (contract-equivalent serialization) failed: %v", err)
	}
}

// FuzzDecodeFeedCacheEnvelope is Property 9: arbitrary byte payloads never
// panic and are accepted only when they form a current, valid envelope.
func FuzzDecodeFeedCacheEnvelope(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("null"),
		[]byte(`{"version":1,"feed":{"notifications":[],"unread_count":0}}`),
		[]byte(`{"version":2,"feed":{"notifications":[],"unread_count":0}}`),
		[]byte(`{"version":1,"feed":{"notifications":null,"unread_count":0}}`),
		[]byte(`{"version":1}`),
		[]byte(`{"feed":{}}`),
		{0x00, 0xff, 0x7f, 0x80},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, payload []byte) {
		feed, classification := decodeFeedCacheEnvelope(payload)
		switch classification {
		case feedCacheDecodeValid:
			if feed.Notifications == nil {
				t.Fatalf("decodeFeedCacheEnvelope() valid classification with nil notifications for payload %q", payload)
			}
			if feed.UnreadCount < 0 {
				t.Fatalf("decodeFeedCacheEnvelope() valid classification with negative unread count for payload %q", payload)
			}
			// A valid classification must re-encode to a payload the decoder also
			// treats as valid and equivalent, proving it reflects a real current
			// schema envelope rather than a lenient accidental parse.
			reencoded, err := encodeFeedCacheEnvelope(feed)
			if err != nil {
				t.Fatalf("encodeFeedCacheEnvelope() of decoded feed error = %v for payload %q", err, payload)
			}
			roundTripFeed, roundTripClass := decodeFeedCacheEnvelope(reencoded)
			if roundTripClass != feedCacheDecodeValid {
				t.Fatalf("re-encoded valid feed decoded as invalid for payload %q", payload)
			}
			if !reflect.DeepEqual(roundTripFeed, feed) {
				t.Fatalf("re-encoded feed mismatch: got %#v, want %#v", roundTripFeed, feed)
			}
		case feedCacheDecodeInvalid:
			if !reflect.DeepEqual(feed, Feed{}) {
				t.Fatalf("decodeFeedCacheEnvelope() invalid classification with non-zero feed %#v for payload %q", feed, payload)
			}
		default:
			t.Fatalf("decodeFeedCacheEnvelope() unknown classification %v for payload %q", classification, payload)
		}
	})
}

// genFeed is a testing/quick-generatable seed that maps deterministically to
// a bounded, valid Feed value.
type genFeed struct {
	Count       uint8
	UnreadCount uint16
}

func (g genFeed) toFeed() Feed {
	const maxNotifications = 5
	count := int(g.Count) % (maxNotifications + 1)

	notifications := make([]Notification, count)
	for i := range notifications {
		notifications[i] = Notification{
			ID:        fmt.Sprintf("notification-%d", i),
			UserID:    fmt.Sprintf("user-%d", i%3),
			Type:      TypeNewApplication,
			Title:     fmt.Sprintf("Title %d", i),
			Message:   fmt.Sprintf("Message body %d", i),
			ActionURL: fmt.Sprintf("/applications/%d", i),
			Read:      i%2 == 0,
			CreatedAt: time.Unix(int64(1_700_000_000+i), 0).UTC(),
		}
		if i%2 == 1 {
			notifications[i].Delivery = &Delivery{
				Channel: DeliveryChannelEmail,
				To:      "delivery-only@example.test",
				Body:    "delivery-only body",
			}
		}
	}

	return Feed{
		Notifications: notifications,
		UnreadCount:   int64(g.UnreadCount),
	}
}

func normalizeQuickLimit(raw int8) int64 {
	return normalizeHandlerLimit(int64(raw))
}

// normalizeHandlerLimit mirrors the handler's existing limit normalization so
// property tests exercise the same canonical variant boundary the endpoint
// uses (default 30, clamp to [1, 100]).
func normalizeHandlerLimit(raw int64) int64 {
	if raw < 1 {
		return 30
	}
	if raw > 100 {
		return 100
	}
	return raw
}
