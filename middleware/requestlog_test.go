package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/contextutil"
	"backend/logging"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

// These tests drive a real echo.New() with real routes through httptest rather
// than calling the middleware closure directly. Two of the behaviours under
// test only exist inside echo's own request pipeline and would be invisible to
// a direct call:
//
//   - c.Path() is populated by echo's router, so the route template of Req 4.8
//     and the unmatched placeholder of Req 4.9 depend on where the middleware
//     sits relative to routing.
//   - the X-Request-Id response header of Req 4.2 must survive echo's
//     centralized error handler and the recover middleware, neither of which
//     runs when a handler is invoked directly.
const (
	// chatsRouteTemplate is the registered template. It is the value Req 4.8
	// requires on request.path.
	chatsRouteTemplate = "/api/v1/job-requests/:jobRequestId/chats"

	// concreteJobRequestID is the parameter value substituted into the request
	// line. Req 4.8 requires it to be absent from request.path, so that Better
	// Stack groups by route instead of fragmenting one line per id.
	concreteJobRequestID = "6653f1c2a8de40b1c0a91f77"

	chatsRequestPath = "/api/v1/job-requests/" + concreteJobRequestID + "/chats"

	testUserID = "507f1f77bcf86cd799439011"
)

// correlation is the snapshot of one request's context taken from inside the
// middleware chain.
type correlation struct {
	captured bool

	id     string
	method string
	path   string
	ok     bool
}

// recordCorrelation captures what the request context carries at its position
// in the chain. It is registered with app.Use immediately after
// RequestContext(), so it also runs for requests that match no route and
// therefore never reach a handler of ours.
func recordCorrelation(dst *correlation) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, method, path, ok := logging.RequestFromContext(c.Request().Context())
			*dst = correlation{captured: true, id: id, method: method, path: path, ok: ok}
			return next(c)
		}
	}
}

// newTestApp builds the application under test.
//
// RequestContext() is registered with app.Use and never with app.Pre: echo
// resolves the route before invoking Use middleware but after invoking Pre
// middleware, so app.Pre would leave c.Path() empty for every request. That
// constraint is itself asserted by TestRequestContextRequiresUseNotPre.
//
// The recover middleware is registered after RequestContext(), matching the
// production order, so a recovered panic still carries the response header.
func newTestApp() (*echo.Echo, *correlation) {
	app := echo.New()
	app.HideBanner = true
	app.HidePort = true

	got := &correlation{}

	app.Use(RequestContext())
	app.Use(recordCorrelation(got))
	app.Use(echomiddleware.RecoverWithConfig(echomiddleware.RecoverConfig{
		StackSize: 8192,
		// Keep the test log readable; the recovery behaviour under test is the
		// response, not echo's stack print.
		DisableStackAll:   true,
		DisablePrintStack: true,
	}))

	app.GET("/ok", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	app.GET("/http-error", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot, "teapot")
	})
	app.GET("/plain-error", func(c echo.Context) error {
		return errors.New("boom")
	})
	app.GET("/panic", func(c echo.Context) error {
		panic("handler exploded")
	})
	app.POST(chatsRouteTemplate, func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	return app, got
}

// do issues one request and returns the recorder. header is applied only when
// set is true, so the "no header supplied at all" case stays distinguishable
// from the "header supplied but empty" case of Req 4.4.
func do(app *echo.Echo, method, target string, set bool, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if set {
		req.Header.Set(echo.HeaderXRequestID, header)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// TestRequestContextAdoptsValidClientHeader asserts that an adoptable
// client-supplied identifier is used byte-for-byte, both in the request context
// and on the response header.
//
// _Requirements: 4.2, 4.3_
func TestRequestContextAdoptsValidClientHeader(t *testing.T) {
	supplied := []struct {
		name  string
		value string
	}{
		{"typical correlation id", "req-0123456789abcdef"},
		{"single character", "a"},
		{"full legal charset", "abcXYZ019-_"},
		{"underscores only", "____"},
		{"200 characters is the upper bound", strings.Repeat("a", 200)},
	}

	for _, tc := range supplied {
		t.Run(tc.name, func(t *testing.T) {
			app, got := newTestApp()
			rec := do(app, http.MethodGet, "/ok", true, tc.value)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !got.captured {
				t.Fatal("the middleware chain did not run")
			}
			if !got.ok {
				t.Fatal("the request context carried no request identifier")
			}
			if got.id != tc.value {
				t.Errorf("context request id = %q, want the supplied value %q byte-for-byte", got.id, tc.value)
			}
			if h := rec.Header().Get(echo.HeaderXRequestID); h != tc.value {
				t.Errorf("X-Request-Id response header = %q, want %q", h, tc.value)
			}
			if got.method != http.MethodGet {
				t.Errorf("context method = %q, want %q", got.method, http.MethodGet)
			}
			if got.path != "/ok" {
				t.Errorf("context path = %q, want %q", got.path, "/ok")
			}
		})
	}
}

// TestRequestContextReplacesUnusableClientHeader asserts that an unusable
// client-supplied identifier is discarded silently and replaced with a
// generated one. The request must still succeed: an illegal header value is
// never a reason to reject a request or to return an error response.
//
// _Requirements: 4.1, 4.2, 4.4_
func TestRequestContextReplacesUnusableClientHeader(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty value", ""},
		{"201 characters exceeds the bound", strings.Repeat("a", 201)},
		{"space is illegal", "abc def"},
		{"dot is illegal", "abc.def"},
		{"slash is illegal", "abc/def"},
		{"newline is illegal", "abc\ndef"},
		{"non-ascii is illegal", "abcé"},
		{"tab is illegal", "abc\tdef"},
		{"null byte is illegal", "abc\x00def"},
		{"leading whitespace is illegal", " abc"},
		{"colon is illegal", "abc:def"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, got := newTestApp()
			rec := do(app, http.MethodGet, "/ok", true, tc.value)

			// Not rejected, and no error response attributable to the header.
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: an invalid X-Request-Id must not reject the request", rec.Code, http.StatusOK)
			}
			if body := rec.Body.String(); body != "ok" {
				t.Errorf("body = %q, want %q: the handler must still have run", body, "ok")
			}

			if !got.ok || got.id == "" {
				t.Fatal("no replacement request identifier was assigned")
			}
			if got.id == tc.value {
				t.Errorf("context request id = %q; the supplied value must have been discarded", got.id)
			}
			if !logging.ValidRequestID(got.id) {
				t.Errorf("generated request id %q is not itself adoptable", got.id)
			}
			if h := rec.Header().Get(echo.HeaderXRequestID); h != got.id {
				t.Errorf("X-Request-Id response header = %q, want the generated id %q", h, got.id)
			}
		})
	}
}

// TestRequestContextGeneratesIDWhenHeaderAbsent covers the ordinary case of a
// client that supplies no header at all.
//
// _Requirements: 4.1, 4.2_
func TestRequestContextGeneratesIDWhenHeaderAbsent(t *testing.T) {
	app, got := newTestApp()
	rec := do(app, http.MethodGet, "/ok", false, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !got.ok || !logging.ValidRequestID(got.id) {
		t.Fatalf("context request id = %q, ok = %v; want a generated adoptable id", got.id, got.ok)
	}
	if h := rec.Header().Get(echo.HeaderXRequestID); h != got.id {
		t.Errorf("X-Request-Id response header = %q, want %q", h, got.id)
	}
}

// TestRequestContextSetsHeaderOnEveryResponseKind asserts the response header is
// present regardless of how the response was produced. The header is set before
// next is called precisely because echo commits the response as soon as a
// handler, the centralized error handler, or the recover middleware writes to
// it, and headers set after that point are discarded.
//
// _Requirements: 4.2, 4.9_
func TestRequestContextSetsHeaderOnEveryResponseKind(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{"handler returning 200", http.MethodGet, "/ok", http.StatusOK},
		{"handler returning an echo.HTTPError", http.MethodGet, "/http-error", http.StatusTeapot},
		{"handler returning a plain error", http.MethodGet, "/plain-error", http.StatusInternalServerError},
		{"unregistered path", http.MethodGet, "/no-such-route/at-all", http.StatusNotFound},
		{"recovered panic", http.MethodGet, "/panic", http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, got := newTestApp()
			rec := do(app, tc.method, tc.target, false, "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			h := rec.Header().Get(echo.HeaderXRequestID)
			if h == "" {
				t.Fatal("X-Request-Id response header is absent")
			}
			if !logging.ValidRequestID(h) {
				t.Errorf("X-Request-Id response header %q is not a valid identifier", h)
			}
			if !got.ok {
				t.Fatal("the request context carried no request identifier")
			}
			if h != got.id {
				t.Errorf("X-Request-Id response header = %q, want the assigned id %q", h, got.id)
			}
		})
	}
}

// TestRequestContextUsesRouteTemplateNotSubstitutedPath asserts request.path is
// the registered template with the parameter left literal.
//
// _Requirements: 4.8_
func TestRequestContextUsesRouteTemplateNotSubstitutedPath(t *testing.T) {
	app, got := newTestApp()
	rec := do(app, http.MethodPost, chatsRequestPath, false, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !got.ok {
		t.Fatal("the request context carried no request identifier")
	}
	if got.path != chatsRouteTemplate {
		t.Errorf("context path = %q, want the route template %q", got.path, chatsRouteTemplate)
	}
	if strings.Contains(got.path, concreteJobRequestID) {
		t.Errorf("context path %q contains the substituted parameter value %q; Better Stack would fragment one entry per id",
			got.path, concreteJobRequestID)
	}
	if got.method != http.MethodPost {
		t.Errorf("context method = %q, want %q", got.method, http.MethodPost)
	}
}

// TestRequestContextUnmatchedRouteUsesPlaceholder asserts that a request
// matching no registered route reports the single fixed placeholder while the
// identifier and method are still populated.
//
// _Requirements: 4.9_
func TestRequestContextUnmatchedRouteUsesPlaceholder(t *testing.T) {
	app, got := newTestApp()
	rec := do(app, http.MethodGet, "/no-such-route/at-all", false, "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !got.captured {
		t.Fatal("the middleware chain did not run for an unmatched route")
	}
	if !got.ok {
		t.Fatal("the request context carried no request identifier")
	}
	if got.path != UnmatchedRoutePath {
		t.Errorf("context path = %q, want the placeholder %q", got.path, UnmatchedRoutePath)
	}
	if got.id == "" || !logging.ValidRequestID(got.id) {
		t.Errorf("context request id = %q, want a valid identifier even on an unmatched route", got.id)
	}
	if got.method != http.MethodGet {
		t.Errorf("context method = %q, want %q", got.method, http.MethodGet)
	}
}

// TestRequestContextOneRequestSharesOneID asserts that every read of the
// request context within one request observes the same identifier, which is
// what makes all Log_Events of one request correlate.
//
// _Requirements: 4.5, 4.11_
func TestRequestContextOneRequestSharesOneID(t *testing.T) {
	app := echo.New()
	app.HideBanner = true

	first := &correlation{}
	app.Use(RequestContext())
	app.Use(recordCorrelation(first))

	var direct, nested string
	app.GET("/two-reads", func(c echo.Context) error {
		ctx := c.Request().Context()

		// One read at the call site.
		id, _, _, ok := logging.RequestFromContext(ctx)
		if !ok {
			t.Error("handler saw no request identifier in the context")
		}
		direct = id

		// One read inside a function invoked with the same context, which is the
		// case Req 4.11 names explicitly.
		nested = readRequestID(ctx)

		return c.String(http.StatusOK, "ok")
	})

	rec := do(app, http.MethodGet, "/two-reads", false, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if direct == "" {
		t.Fatal("the direct context read produced an empty identifier")
	}
	if direct != nested {
		t.Errorf("two reads in one request disagree: %q then %q", direct, nested)
	}
	if first.id != direct {
		t.Errorf("middleware read %q but handler read %q; one request must carry exactly one identifier", first.id, direct)
	}
	if h := rec.Header().Get(echo.HeaderXRequestID); h != direct {
		t.Errorf("X-Request-Id response header = %q, want the same identifier %q", h, direct)
	}
}

// readRequestID stands in for any function invoked with the request context.
func readRequestID(ctx context.Context) string {
	id, _, _, _ := logging.RequestFromContext(ctx)
	return id
}

// TestRequestContextUserCorrelation asserts that a route which authenticates —
// simulated here by calling contextutil.SetUser, exactly as JWTAuth does before
// next(c) — carries the user identifier in the request context, and that a
// public route reports its absence.
//
// _Requirements: 4.6, 4.10_
func TestRequestContextUserCorrelation(t *testing.T) {
	app := echo.New()
	app.HideBanner = true
	app.Use(RequestContext())

	// authenticate mirrors what JWTAuth does once a token has been validated.
	authenticate := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			contextutil.SetUser(c, testUserID)
			return next(c)
		}
	}

	type observation struct {
		requestID string
		requestOK bool
		userID    string
		userOK    bool
	}
	var protected, public observation

	observe := func(dst *observation) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			id, _, _, ok := logging.RequestFromContext(ctx)
			userID, userOK := logging.UserIDFromContext(ctx)
			*dst = observation{requestID: id, requestOK: ok, userID: userID, userOK: userOK}
			return c.String(http.StatusOK, "ok")
		}
	}

	app.GET("/protected", observe(&protected), authenticate)
	app.GET("/public", observe(&public))

	if rec := do(app, http.MethodGet, "/protected", false, ""); rec.Code != http.StatusOK {
		t.Fatalf("protected route status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec := do(app, http.MethodGet, "/public", false, ""); rec.Code != http.StatusOK {
		t.Fatalf("public route status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Authenticated route: user id present, byte-for-byte, alongside the request id.
	if !protected.userOK {
		t.Error("authenticated route reported no user identifier in the request context")
	}
	if protected.userID != testUserID {
		t.Errorf("authenticated route user id = %q, want %q", protected.userID, testUserID)
	}
	if !protected.requestOK || protected.requestID == "" {
		t.Error("authenticated route lost the request identifier when the user id was added")
	}

	// Public route: no user object, but correlation is otherwise intact.
	if public.userOK || public.userID != "" {
		t.Errorf("public route reported user id %q, ok = %v; want absence", public.userID, public.userOK)
	}
	if !public.requestOK || public.requestID == "" {
		t.Error("public route carried no request identifier")
	}
	if protected.requestID == public.requestID {
		t.Errorf("two requests shared the identifier %q", protected.requestID)
	}
}

// TestRequestContextGeneratesDistinctIDsPerRequest asserts that identifiers are
// distinct across requests handled by one process instance.
//
// _Requirements: 4.1_
func TestRequestContextGeneratesDistinctIDsPerRequest(t *testing.T) {
	const requests = 100

	app, _ := newTestApp()
	seen := make(map[string]struct{}, requests)

	for i := 0; i < requests; i++ {
		rec := do(app, http.MethodGet, "/ok", false, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i, rec.Code, http.StatusOK)
		}
		id := rec.Header().Get(echo.HeaderXRequestID)
		if !logging.ValidRequestID(id) {
			t.Fatalf("request %d: id %q is not valid", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("request %d reused the identifier %q", i, id)
		}
		seen[id] = struct{}{}
	}

	if len(seen) != requests {
		t.Fatalf("collected %d distinct identifiers across %d requests", len(seen), requests)
	}
}

// TestRequestContextRequiresUseNotPre pins the reason RequestContext must be
// registered with app.Use.
//
// Echo resolves the route after running Pre middleware and before running Use
// middleware. Under app.Pre, c.Path() is therefore still empty for every
// request, and the route template Req 4.8 requires degrades silently to the
// Req 4.9 unmatched placeholder: the middleware still runs, the header is still
// set, the handler still returns 200, and only request.path is quietly wrong.
//
// This test fails loudly if a future refactor moves the registration to
// app.Pre.
//
// _Requirements: 4.8, 4.9_
func TestRequestContextRequiresUseNotPre(t *testing.T) {
	// The failure mode: registered with app.Pre.
	pre := echo.New()
	pre.HideBanner = true
	preGot := &correlation{}
	pre.Pre(RequestContext())
	pre.Use(recordCorrelation(preGot))
	pre.POST(chatsRouteTemplate, func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	rec := do(pre, http.MethodPost, chatsRequestPath, false, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("app.Pre: status = %d, want %d; the route was expected to match", rec.Code, http.StatusOK)
	}
	if !preGot.ok {
		t.Fatal("app.Pre: the request context carried no request identifier")
	}
	if preGot.path != UnmatchedRoutePath {
		t.Errorf("app.Pre: context path = %q, want %q.\n"+
			"Echo populates c.Path() only after routing, so Pre registration cannot see the route template. "+
			"If this assertion now fails because the template was captured, echo's ordering changed and the "+
			"app.Use constraint documented on RequestContext should be revisited.",
			preGot.path, UnmatchedRoutePath)
	}
	if h := rec.Header().Get(echo.HeaderXRequestID); h == "" {
		t.Error("app.Pre: X-Request-Id response header is absent, so the misregistration would be silent")
	}

	// The correct wiring, for contrast: the same request yields the template.
	use, useGot := newTestApp()
	if rec := do(use, http.MethodPost, chatsRequestPath, false, ""); rec.Code != http.StatusOK {
		t.Fatalf("app.Use: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if useGot.path != chatsRouteTemplate {
		t.Errorf("app.Use: context path = %q, want the route template %q", useGot.path, chatsRouteTemplate)
	}
}
