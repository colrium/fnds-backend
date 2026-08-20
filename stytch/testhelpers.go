package stytch

import (
	"sync"
	"sync/atomic"
	"time"
)

// FakeClock is a Clock implementation advanced explicitly, so time-dependent
// behaviors (15-minute challenge expiry, 7-day invitation expiry, 72-hour reaper,
// 7-day deletion window, 30-second cache bound) are testable in microseconds
// with no test sleeping longer than 50ms.
type FakeClock struct {
	now atomic.Value // stores time.Time
}

// NewFakeClock creates a FakeClock set to the given time.
func NewFakeClock(t time.Time) *FakeClock {
	c := &FakeClock{}
	c.now.Store(t)
	return c
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	return c.now.Load().(time.Time)
}

// Advance moves the clock forward by the given duration.
func (c *FakeClock) Advance(d time.Duration) {
	c.now.Store(c.Now().Add(d))
}

// --- Notification Spy ---
//
// DEFERRED SCAFFOLDING — do not delete as dead code.
//
// NotificationSpy implements the `workspaces.Notifier` interface
// (SendEmail/SendSMS) and is the spy required by spec task 3.7 ("Implement
// fakeClock and the shared test helpers") for the notification assertions in
// the not-yet-written test tasks 8.x, 12.x, and 21.x — the link email/SMS, the
// removal and role-change emails, the ownership-transfer emails, and the
// migration email. Those tasks are still open, so the spy has no caller yet.
// See design.md *Test infrastructure* -> "Notification spy".
//
// It lives in the non-test build so other packages' tests can use it without a
// cyclic test-only import, exactly like `stytch.Fake`. Per task 3.6's
// completion criterion, this is a stopgap and NOT acceptable long-term: once
// the real HTTP client is wired everywhere, move this file and fake.go to a
// test-only `stytch/fake` package (or gate them behind a build tag) so neither
// is reachable from the production binary.

// EmailRecord records one email sent through the NotificationSpy.
type EmailRecord struct {
	To      string
	Subject string
	Body    string
	SentAt  time.Time
}

// SMSRecord records one SMS sent through the NotificationSpy.
type SMSRecord struct {
	To     string
	Body   string
	SentAt time.Time
}

// NotificationSpy records notifications for assertion in tests.
// It is shipped in the non-test build so other packages' tests can use it
// without cyclic test-only imports.
type NotificationSpy struct {
	mu     sync.Mutex
	clock  Clock
	Emails []EmailRecord
	SMSes  []SMSRecord
}

// NewNotificationSpy creates a NotificationSpy that timestamps sends using
// the provided clock. If clock is nil, SystemClock() is used.
func NewNotificationSpy(clock Clock) *NotificationSpy {
	if clock == nil {
		clock = SystemClock()
	}
	return &NotificationSpy{
		clock: clock,
	}
}

// SendEmail records an email notification.
func (s *NotificationSpy) SendEmail(to, subject, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Emails = append(s.Emails, EmailRecord{
		To:      to,
		Subject: subject,
		Body:    body,
		SentAt:  s.clock.Now(),
	})
}

// SendSMS records an SMS notification.
func (s *NotificationSpy) SendSMS(to, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SMSes = append(s.SMSes, SMSRecord{
		To:     to,
		Body:   body,
		SentAt: s.clock.Now(),
	})
}

// EmailCount returns the number of emails recorded.
func (s *NotificationSpy) EmailCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Emails)
}

// SMSCount returns the number of SMSes recorded.
func (s *NotificationSpy) SMSCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.SMSes)
}

// LastEmail returns the most recently recorded email and true, or the zero
// value and false if no emails have been recorded.
func (s *NotificationSpy) LastEmail() (EmailRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Emails) == 0 {
		return EmailRecord{}, false
	}
	return s.Emails[len(s.Emails)-1], true
}

// LastSMS returns the most recently recorded SMS and true, or the zero
// value and false if no SMSes have been recorded.
func (s *NotificationSpy) LastSMS() (SMSRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.SMSes) == 0 {
		return SMSRecord{}, false
	}
	return s.SMSes[len(s.SMSes)-1], true
}

// Reset clears all recorded emails and SMSes.
func (s *NotificationSpy) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Emails = nil
	s.SMSes = nil
}
