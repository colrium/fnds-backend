package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/configs"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"golang.org/x/crypto/bcrypt"
)

const (
	cutoverTestPassword = "Password1!"
	cutoverTestEmail    = "employer@example.com"
	cutoverTestUserID   = "user-1"
)

// useCutoverTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors usePostAuthTestMongo in workspaces.
func useCutoverTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// cutoverUserDoc builds a `users` document that can log in successfully: active,
// not legacy-migrated, and holding a bcrypt hash of cutoverTestPassword.
func cutoverUserDoc(t testing.TB, userType string) bson.D {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(cutoverTestPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return bson.D{
		{Key: "_id", Value: cutoverTestUserID},
		{Key: "email", Value: cutoverTestEmail},
		{Key: "password", Value: hash},
		{Key: "user_type", Value: userType},
		{Key: "status", Value: users.StatusActive},
		{Key: "is_active", Value: true},
	}
}

// runLogin runs HandleUserLogin against a real echo context and returns the
// recorder so assertions can read the status and body.
func runLogin(t testing.TB) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"` + cutoverTestEmail + `","password":"` + cutoverTestPassword + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if err := HandleUserLogin(c); err != nil {
		t.Fatalf("HandleUserLogin returned an error: %v", err)
	}
	return rec
}

type cutoverErrorBody struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func decodeCutoverError(t testing.TB, rec *httptest.ResponseRecorder) cutoverErrorBody {
	t.Helper()
	var body cutoverErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// Req 21.11: once LEGACY_EMPLOYER_AUTH_ENABLED is off, a migrated employer's old
// password must not sign them in — they get 409 use_stytch_sign_in instead.
func TestHandleUserLogin_MigratedEmployerAfterCutover(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("migrated employer is refused with use_stytch_sign_in", func(mt *mtest.T) {
		useCutoverTestMongo(mt)
		mt.Setenv("SESSION_KEY", "test-secret")
		mt.Setenv("LEGACY_EMPLOYER_AUTH_ENABLED", "false")

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, cutoverUserDoc(mt, "customer")),
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: cutoverTestUserID},
			}),
		)

		rec := runLogin(mt)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		body := decodeCutoverError(mt, rec)
		if body.Success {
			t.Error("success = true, want false")
		}
		if body.Error.Code != "use_stytch_sign_in" {
			t.Errorf("error.code = %q, want %q", body.Error.Code, "use_stytch_sign_in")
		}
		if got := body.Error.Details["sign_in_url"]; got != "/employer/sign-up" {
			t.Errorf("error.details.sign_in_url = %v, want %q", got, "/employer/sign-up")
		}
		if strings.Contains(rec.Body.String(), "access_token") {
			t.Error("response must not contain a token for a migrated employer")
		}
	})
}

// Req 22.2: a `users` document holding zero Account_Links keeps the legacy path,
// even after cutover.
func TestHandleUserLogin_UnmigratedCustomerAfterCutover(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("customer with no account link logs in normally", func(mt *mtest.T) {
		useCutoverTestMongo(mt)
		mt.Setenv("SESSION_KEY", "test-secret")
		mt.Setenv("LEGACY_EMPLOYER_AUTH_ENABLED", "false")

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, cutoverUserDoc(mt, "customer")),
			// Empty first batch = no account_links document = not migrated.
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch),
		)

		rec := runLogin(mt)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "access_token") {
			t.Errorf("expected a token in the response, got %s", rec.Body.String())
		}
	})
}

// Req 22.2 / 22.5: non-customer logins must be untouched. Only one mock response
// is queued, so a stray account_links read would fail the operation and — because
// IsUserMigrated fails closed — turn this into a 409.
func TestHandleUserLogin_NonCustomerTypesUnaffectedAfterCutover(t *testing.T) {
	for _, userType := range []string{"artisan", "assessor", "admin"} {
		t.Run(userType, func(t *testing.T) {
			mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

			mt.Run("logs in normally", func(mt *mtest.T) {
				useCutoverTestMongo(mt)
				mt.Setenv("SESSION_KEY", "test-secret")
				mt.Setenv("LEGACY_EMPLOYER_AUTH_ENABLED", "false")

				ns := func(coll string) string { return mt.DB.Name() + "." + coll }
				mt.AddMockResponses(
					mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, cutoverUserDoc(mt, userType)),
				)

				rec := runLogin(mt)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "access_token") {
					t.Errorf("expected a token in the response, got %s", rec.Body.String())
				}
			})
		})
	}
}

// Req 21.12: flipping LEGACY_EMPLOYER_AUTH_ENABLED back on restores the old
// sign-in path for a migrated employer, with no other change. The single queued
// mock response also proves the account_links read is skipped entirely while the
// flag is on.
func TestHandleUserLogin_MigratedEmployerWhileLegacyEnabled(t *testing.T) {
	for _, flag := range []string{"true", "1"} {
		t.Run("flag="+flag, func(t *testing.T) {
			mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

			mt.Run("migrated employer still logs in", func(mt *mtest.T) {
				useCutoverTestMongo(mt)
				mt.Setenv("SESSION_KEY", "test-secret")
				mt.Setenv("LEGACY_EMPLOYER_AUTH_ENABLED", flag)

				ns := func(coll string) string { return mt.DB.Name() + "." + coll }
				mt.AddMockResponses(
					mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, cutoverUserDoc(mt, "customer")),
				)

				rec := runLogin(mt)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "access_token") {
					t.Errorf("expected a token in the response, got %s", rec.Body.String())
				}
			})
		})
	}
}

// The cutover refusal must win over the legacy-platform password-reset steer: a
// migrated employer who also carries users.migrated=true belongs on the Stytch
// path, not in an OTP reset flow that ends in a legacy session.
func TestHandleUserLogin_CutoverBeatsLegacyPasswordReset(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("migrated flag does not bypass the cutover guard", func(mt *mtest.T) {
		useCutoverTestMongo(mt)
		mt.Setenv("SESSION_KEY", "test-secret")
		mt.Setenv("LEGACY_EMPLOYER_AUTH_ENABLED", "false")

		doc := append(cutoverUserDoc(mt, "customer"), bson.E{Key: "migrated", Value: true})

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, doc),
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: cutoverTestUserID},
			}),
		)

		rec := runLogin(mt)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if code := decodeCutoverError(mt, rec).Error.Code; code != "use_stytch_sign_in" {
			t.Errorf("error.code = %q, want %q", code, "use_stytch_sign_in")
		}
	})
}
