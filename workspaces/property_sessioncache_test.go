package workspaces

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// cacheFakeClock is a controllable stytch.Clock for the session cache properties.
type cacheFakeClock struct{ now time.Time }

func (c *cacheFakeClock) Now() time.Time { return c.now }

// Feature: stytch-employer-onboarding, Property 19: Session_Resolver caching never serves stale authority beyond the bound
// Validates: Requirements 16.7, 16.8, 16.9, 17.2, 17.3, 17.4, 17.8, 17.9
func TestProperty19_SessionCacheNeverServesStaleAuthorityBeyondBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		clock := &cacheFakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		ttl := 30 * time.Second
		cache := NewSessionCache(ttl, clock)

		userID := "user-1"
		workspaceID := "ws-1"

		version := int64(1)
		sc := SessionContext{
			UserID:           userID,
			WorkspaceID:      workspaceID,
			Role:             RoleAdmin,
			AuthorityVersion: version,
		}
		cache.Set(sc)
		setAt := clock.now

		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.IntRange(0, 2).Draw(t, "op")
			switch op {
			case 0: // advance clock by a bounded amount
				advanceSeconds := rapid.IntRange(0, 120).Draw(t, "advanceSeconds")
				clock.now = clock.now.Add(time.Duration(advanceSeconds) * time.Second)
			case 1: // change authority version (simulating a Membership role/status change)
				version++
				sc.AuthorityVersion = version
				sc.Role = RoleFinance
				// Suspension/removal takes effect on the NEXT request with zero
				// clock advance: the caller invalidates the cache synchronously
				// (MembershipService.invalidateAuthority), which this models
				// directly since SessionCache itself has no invalidation hook
				// tied to authority change beyond the explicit Invalidate call.
				cache.Invalidate(userID, workspaceID)
				cache.Set(sc)
				setAt = clock.now
			case 2: // read
				got, hit := cache.Get(userID, workspaceID, version)
				if hit {
					// A hit must never carry an AuthorityVersion different from
					// the caller-supplied current version (Req 16.7, 16.8).
					if got.AuthorityVersion != version {
						t.Fatalf("step %d: cache hit returned AuthorityVersion %d, want %d", i, got.AuthorityVersion, version)
					}
					// The entry must never be served past its TTL bound (Req 17.8).
					if clock.now.Sub(setAt) > ttl {
						t.Fatalf("step %d: cache hit %v after it was set, exceeds the %v bound", i, clock.now.Sub(setAt), ttl)
					}
				}
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 19b: an entry past its TTL is always a miss, regardless of AuthorityVersion match
// Validates: Requirement 17.8
func TestProperty19_EntryPastTTLIsAlwaysAMiss(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		clock := &cacheFakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		ttl := 30 * time.Second
		cache := NewSessionCache(ttl, clock)

		version := rapid.Int64Range(1, 1000).Draw(t, "version")
		sc := SessionContext{UserID: "u", WorkspaceID: "w", AuthorityVersion: version}
		cache.Set(sc)

		overshootSeconds := rapid.IntRange(1, 600).Draw(t, "overshootSeconds")
		clock.now = clock.now.Add(ttl).Add(time.Duration(overshootSeconds) * time.Second)

		_, hit := cache.Get("u", "w", version)
		if hit {
			t.Fatalf("cache hit %v past TTL %v, want a miss", time.Duration(overshootSeconds)*time.Second+ttl, ttl)
		}
	})
}

// Feature: stytch-employer-onboarding, Property 19c: a cache hit requires exactly the current AuthorityVersion
// Validates: Requirement 16.7
func TestProperty19_HitRequiresExactAuthorityVersionMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		clock := &cacheFakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		cache := NewSessionCache(30*time.Second, clock)

		storedVersion := rapid.Int64Range(1, 1000).Draw(t, "storedVersion")
		queryVersion := rapid.Int64Range(1, 1000).Draw(t, "queryVersion")

		cache.Set(SessionContext{UserID: "u", WorkspaceID: "w", AuthorityVersion: storedVersion})

		_, hit := cache.Get("u", "w", queryVersion)
		if storedVersion == queryVersion {
			if !hit {
				t.Fatalf("expected a hit when queryVersion equals storedVersion (%d)", storedVersion)
			}
		} else {
			if hit {
				t.Fatalf("expected a miss when queryVersion (%d) != storedVersion (%d)", queryVersion, storedVersion)
			}
		}
	})
}
