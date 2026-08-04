package notifications

import (
	"log/slog"
	"time"
)

// SlogCacheObserver implements cacheObserver using structured slog events.
// It records bounded outcome values only — no user IDs, tokens, notification
// IDs, Redis URLs, cache keys, or payloads — so it is safe for production
// logs. Warnings mark runtime failures the caller has already handled by
// falling open; debug events mark routine outcomes for local diagnosis.
type SlogCacheObserver struct{}

// NewSlogCacheObserver constructs the default production cache observer.
func NewSlogCacheObserver() *SlogCacheObserver {
	return &SlogCacheObserver{}
}

func (SlogCacheObserver) RecordLookup(outcome string, duration time.Duration) {
	if outcome == "error" {
		slog.Warn("notifications cache lookup degraded", "outcome", outcome, "duration_ms", duration.Milliseconds())
		return
	}
	slog.Debug("notifications cache lookup", "outcome", outcome, "duration_ms", duration.Milliseconds())
}

func (SlogCacheObserver) RecordSourceLoad(duration time.Duration, errorClass string) {
	if errorClass == "error" {
		slog.Warn("notifications source load failed", "duration_ms", duration.Milliseconds())
		return
	}
	slog.Debug("notifications source load", "duration_ms", duration.Milliseconds())
}

func (SlogCacheObserver) RecordWrite(outcome string) {
	if outcome == "error" {
		slog.Warn("notifications cache write degraded", "outcome", outcome)
		return
	}
	slog.Debug("notifications cache write", "outcome", outcome)
}

func (SlogCacheObserver) RecordInvalidation(outcome string, userCount int) {
	if outcome == "error" {
		slog.Warn("notifications cache invalidation degraded", "outcome", outcome, "user_count", userCount)
		return
	}
	slog.Debug("notifications cache invalidation", "outcome", outcome, "user_count", userCount)
}

func (SlogCacheObserver) RecordCoalescedWait() {
	slog.Debug("notifications cache request coalesced")
}

var _ cacheObserver = SlogCacheObserver{}
