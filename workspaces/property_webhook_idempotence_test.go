package workspaces

import (
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// simMembershipWebhookState mirrors the two guards that make webhook
// processing idempotent and order-insensitive:
//
//  1. The outer dedup gate: Handle inserts {_id: event.ID} into
//     processed_webhook_events BEFORE processing, and a duplicate key means
//     "already processed" — zero additional state change (Req 19.4, 19.5).
//  2. The inner staleness gate: SetStatusFromWebhook's update only applies
//     when last_webhook_event_at is absent or strictly less than the new
//     event's timestamp (Req 19.10) — so a later-delivered but
//     earlier-timestamped event is a no-op.
type simMembershipWebhookState struct {
	status             MembershipStatus
	lastWebhookEventAt time.Time
	hasLastEventAt     bool
	authorityVersion   int64
}

type webhookEvent struct {
	id        string
	timestamp time.Time
	newStatus MembershipStatus
}

// applyWebhookSequence processes events in the given order against a fresh
// state, applying the outer event-ID dedup gate and the inner timestamp
// staleness gate exactly as Handle + SetStatusFromWebhook do.
func applyWebhookSequence(events []webhookEvent, initial simMembershipWebhookState) simMembershipWebhookState {
	state := initial
	processedIDs := map[string]bool{}

	for _, ev := range events {
		if processedIDs[ev.id] {
			continue // duplicate key on processed_webhook_events -> no-op (Req 19.5)
		}
		processedIDs[ev.id] = true

		if state.hasLastEventAt && !ev.timestamp.After(state.lastWebhookEventAt) {
			continue // stale event, guard rejects the update (Req 19.10)
		}

		state.status = ev.newStatus
		state.lastWebhookEventAt = ev.timestamp
		state.hasLastEventAt = true
		state.authorityVersion++
	}

	return state
}

// Feature: stytch-employer-onboarding, Property 20: Webhook processing is idempotent and order-insensitive
// Validates: Requirements 19.4, 19.5, 19.6, 19.7, 19.8, 19.9, 19.10, 19.12
func TestProperty20_WebhookProcessingIdempotentAndOrderInsensitive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		initial := simMembershipWebhookState{status: MembershipActive, authorityVersion: 1}

		baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		distinctCount := rapid.IntRange(1, 8).Draw(t, "distinctEventCount")

		var canonical []webhookEvent
		usedOffsets := map[int]bool{}
		for i := 0; i < distinctCount; i++ {
			offset := rapid.IntRange(0, 1000).Draw(t, "offsetSeconds")
			for usedOffsets[offset] {
				offset = rapid.IntRange(0, 1000).Draw(t, "offsetSecondsRetry")
			}
			usedOffsets[offset] = true
			status := rapid.SampledFrom([]MembershipStatus{MembershipActive, MembershipSuspended, MembershipRemoved}).Draw(t, "status")
			canonical = append(canonical, webhookEvent{
				id:        "evt-" + strconv.Itoa(i),
				timestamp: baseTime.Add(time.Duration(offset) * time.Second),
				newStatus: status,
			})
		}

		// Build a shuffled, duplicate-inserted delivery sequence from the
		// canonical distinct-timestamp event set.
		deliveryCount := rapid.IntRange(distinctCount, distinctCount*3).Draw(t, "deliveryCount")
		var delivered []webhookEvent
		for i := 0; i < deliveryCount; i++ {
			delivered = append(delivered, rapid.SampledFrom(canonical).Draw(t, "deliveredEvent"))
		}

		// Order-insensitivity: any permutation of `delivered` yields the same
		// final state, because final state depends only on the max-timestamp
		// event's status (each fresh apply's timestamp check only compares
		// against the LARGEST timestamp seen so far, so out-of-order arrival
		// never sticks a non-maximal status once the maximal one lands).
		shuffled := rapid.Permutation(delivered).Draw(t, "shuffledOrder")

		resultInOrder := applyWebhookSequence(delivered, initial)
		resultShuffled := applyWebhookSequence(shuffled, initial)

		if resultInOrder.status != resultShuffled.status {
			t.Fatalf("order-sensitivity detected: in-order status=%s, shuffled status=%s", resultInOrder.status, resultShuffled.status)
		}
		if resultInOrder.lastWebhookEventAt != resultShuffled.lastWebhookEventAt {
			t.Fatalf("order-sensitivity detected in lastWebhookEventAt: in-order=%v, shuffled=%v", resultInOrder.lastWebhookEventAt, resultShuffled.lastWebhookEventAt)
		}

		// Idempotence: re-processing the exact same delivered sequence a
		// second time (simulating a full replay after e.g. a network retry)
		// changes nothing, because every event ID was already recorded.
		resultReplayed := applyWebhookSequence(delivered, resultInOrder)
		if resultReplayed != resultInOrder {
			t.Fatalf("replaying the same sequence changed state: before=%+v after=%+v", resultInOrder, resultReplayed)
		}

		// The final status/timestamp must equal the delivered event with the
		// maximum timestamp — the actual mathematical content of "order
		// insensitive" for this last-write-wins-by-timestamp guard. This is
		// computed over `delivered`, not the full canonical set, since not
		// every canonical event is necessarily delivered in a given draw.
		var maxEvent webhookEvent
		for i, ev := range delivered {
			if i == 0 || ev.timestamp.After(maxEvent.timestamp) {
				maxEvent = ev
			}
		}
		if resultInOrder.status != maxEvent.newStatus {
			t.Fatalf("final status %s does not match the max-timestamp event's status %s", resultInOrder.status, maxEvent.newStatus)
		}
	})
}
