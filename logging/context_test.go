package logging

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestContextWithRequestRoundTrip(t *testing.T) {
	ctx := ContextWithRequest(context.Background(), "abc-123", "POST", "/api/v1/job-requests/:jobRequestId/chats")

	id, method, path, ok := RequestFromContext(ctx)
	if !ok {
		t.Fatal("RequestFromContext reported ok == false for a context carrying a request")
	}
	if id != "abc-123" {
		t.Errorf("id = %q, want %q", id, "abc-123")
	}
	if method != "POST" {
		t.Errorf("method = %q, want %q", method, "POST")
	}
	if want := "/api/v1/job-requests/:jobRequestId/chats"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestContextWithRequestOverwritesEarlierValue(t *testing.T) {
	ctx := ContextWithRequest(context.Background(), "first", "GET", "/a")
	ctx = ContextWithRequest(ctx, "second", "PUT", "/b")

	id, method, path, ok := RequestFromContext(ctx)
	if !ok || id != "second" || method != "PUT" || path != "/b" {
		t.Fatalf("got (%q, %q, %q, %v), want (\"second\", \"PUT\", \"/b\", true)", id, method, path, ok)
	}
}

func TestContextWithUserIDRoundTrip(t *testing.T) {
	ctx := ContextWithUserID(context.Background(), "507f1f77bcf86cd799439011")

	got, ok := UserIDFromContext(ctx)
	if !ok {
		t.Fatal("UserIDFromContext reported ok == false for a context carrying a user id")
	}
	if want := "507f1f77bcf86cd799439011"; got != want {
		t.Errorf("user id = %q, want %q", got, want)
	}
}

func TestContextCarriesRequestAndUserIndependently(t *testing.T) {
	ctx := ContextWithRequest(context.Background(), "rid", "GET", "/x")
	ctx = ContextWithUserID(ctx, "uid")

	if id, _, _, ok := RequestFromContext(ctx); !ok || id != "rid" {
		t.Errorf("request id = %q, ok = %v; want \"rid\", true", id, ok)
	}
	if uid, ok := UserIDFromContext(ctx); !ok || uid != "uid" {
		t.Errorf("user id = %q, ok = %v; want \"uid\", true", uid, ok)
	}
}

func TestContextValuesAbsent(t *testing.T) {
	// A background context carries no correlation, which is the state of every
	// slog.Error call site that passes no request context (Req 4.7, 4.10).
	if id, method, path, ok := RequestFromContext(context.Background()); ok || id != "" || method != "" || path != "" {
		t.Errorf("RequestFromContext(background) = (%q, %q, %q, %v); want empty, false", id, method, path, ok)
	}
	if uid, ok := UserIDFromContext(context.Background()); ok || uid != "" {
		t.Errorf("UserIDFromContext(background) = (%q, %v); want \"\", false", uid, ok)
	}

	// A context carrying only a user id still reports no request, and vice versa.
	userOnly := ContextWithUserID(context.Background(), "uid")
	if _, _, _, ok := RequestFromContext(userOnly); ok {
		t.Error("RequestFromContext reported ok == true for a user-only context")
	}
	requestOnly := ContextWithRequest(context.Background(), "rid", "GET", "/x")
	if _, ok := UserIDFromContext(requestOnly); ok {
		t.Error("UserIDFromContext reported ok == true for a request-only context")
	}
}

func TestContextWithEmptyValuesStoresNothing(t *testing.T) {
	// An empty id means "no identifier", so lookups must report absence rather
	// than an empty request.id or user.id reaching a Log_Event (Req 4.7, 4.10).
	if _, _, _, ok := RequestFromContext(ContextWithRequest(context.Background(), "", "GET", "/x")); ok {
		t.Error("an empty request id was stored in the context")
	}
	if _, ok := UserIDFromContext(ContextWithUserID(context.Background(), "")); ok {
		t.Error("an empty user id was stored in the context")
	}
}

//nolint:staticcheck // passing a nil context is exactly what is under test.
func TestContextHelpersTolerateNilContext(t *testing.T) {
	if _, _, _, ok := RequestFromContext(nil); ok {
		t.Error("RequestFromContext(nil) reported ok == true")
	}
	if _, ok := UserIDFromContext(nil); ok {
		t.Error("UserIDFromContext(nil) reported ok == true")
	}

	ctx := ContextWithRequest(nil, "rid", "GET", "/x")
	if ctx == nil {
		t.Fatal("ContextWithRequest(nil, ...) returned a nil context")
	}
	if id, _, _, ok := RequestFromContext(ctx); !ok || id != "rid" {
		t.Errorf("request id = %q, ok = %v; want \"rid\", true", id, ok)
	}

	ctx = ContextWithUserID(nil, "uid")
	if ctx == nil {
		t.Fatal("ContextWithUserID(nil, ...) returned a nil context")
	}
	if uid, ok := UserIDFromContext(ctx); !ok || uid != "uid" {
		t.Errorf("user id = %q, ok = %v; want \"uid\", true", uid, ok)
	}
}

func TestFromContextReturnsUsableLogger(t *testing.T) {
	// FromContext is sugar only: correlation does not depend on it. The contract
	// checked here is that it never returns nil, with or without values in ctx.
	if FromContext(context.Background()) == nil {
		t.Error("FromContext(background) returned nil")
	}
	//nolint:staticcheck // nil context tolerance is part of the contract.
	if FromContext(nil) == nil {
		t.Error("FromContext(nil) returned nil")
	}

	ctx := ContextWithUserID(ContextWithRequest(context.Background(), "rid", "GET", "/x"), "uid")
	if FromContext(ctx) == nil {
		t.Error("FromContext(correlated ctx) returned nil")
	}
}

func TestValidRequestIDBoundaries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty is rejected", "", false},
		{"single character", "a", true},
		{"single digit", "0", true},
		{"single hyphen", "-", true},
		{"single underscore", "_", true},
		{"full legal charset", "abcXYZ019-_", true},
		{"length 199", strings.Repeat("a", 199), true},
		{"length 200 is the upper bound", strings.Repeat("a", 200), true},
		{"length 201 is rejected", strings.Repeat("a", 201), false},
		{"generated id is adoptable", NewRequestID(), true},
		{"space is rejected", "abc def", false},
		{"dot is rejected", "abc.def", false},
		{"slash is rejected", "abc/def", false},
		{"colon is rejected", "abc:def", false},
		{"plus is rejected", "abc+def", false},
		{"newline is rejected", "abc\ndef", false},
		{"tab is rejected", "abc\tdef", false},
		{"null byte is rejected", "abc\x00def", false},
		{"leading whitespace is rejected", " abc", false},
		{"trailing whitespace is rejected", "abc ", false},
		{"non-ascii letter is rejected", "abcé", false},
		{"invalid utf-8 is rejected", "abc\xff", false},
		{"200 bytes of multi-byte runes is rejected", strings.Repeat("é", 100), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRequestID(tc.in); got != tc.want {
				t.Errorf("ValidRequestID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewRequestIDFormat(t *testing.T) {
	const (
		minLen = 8   // Req 4.1 lower bound
		maxLen = 200 // Req 4.1 upper bound
	)

	for i := 0; i < 1000; i++ {
		id := NewRequestID()

		if len(id) < minLen || len(id) > maxLen {
			t.Fatalf("len(%q) = %d, want between %d and %d", id, len(id), minLen, maxLen)
		}
		// The generated form is hex(16-byte nonce) + "-" + base36 counter, so the
		// length is 34 to 46 characters.
		if len(id) < 34 || len(id) > 46 {
			t.Fatalf("len(%q) = %d, want between 34 and 46 for the documented format", id, len(id))
		}
		if !ValidRequestID(id) {
			t.Fatalf("ValidRequestID(%q) = false; a generated id must be charset compliant", id)
		}

		nonce, counter, found := strings.Cut(id, "-")
		if !found {
			t.Fatalf("id %q has no '-' separator", id)
		}
		if len(nonce) != 2*nonceBytes {
			t.Fatalf("nonce %q has length %d, want %d hex characters", nonce, len(nonce), 2*nonceBytes)
		}
		if counter == "" {
			t.Fatalf("id %q has an empty counter segment", id)
		}
	}
}

func TestNewRequestIDSharesOneProcessNonce(t *testing.T) {
	// The nonce is generated once at package init, not per call, so every id in
	// one process shares a prefix and only the counter varies.
	first, _, _ := strings.Cut(NewRequestID(), "-")
	for i := 0; i < 100; i++ {
		nonce, _, _ := strings.Cut(NewRequestID(), "-")
		if nonce != first {
			t.Fatalf("nonce changed between calls: %q then %q", first, nonce)
		}
	}
}

func TestNewRequestIDUniqueUnderConcurrency(t *testing.T) {
	const (
		goroutines = 64
		perRoutine = 200
	)

	batches := make([][]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			ids := make([]string, perRoutine)
			for i := range ids {
				ids[i] = NewRequestID()
			}
			batches[g] = ids
		}(g)
	}
	wg.Wait()

	seen := make(map[string]struct{}, goroutines*perRoutine)
	for _, batch := range batches {
		for _, id := range batch {
			if !ValidRequestID(id) {
				t.Fatalf("ValidRequestID(%q) = false", id)
			}
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate request id generated: %q", id)
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != goroutines*perRoutine {
		t.Fatalf("collected %d distinct ids, want %d", len(seen), goroutines*perRoutine)
	}
}
