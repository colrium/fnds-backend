package jobrequests

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/configs"
	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// An employer may hold one company profile. The pre-existing guard only rejected
// a second company sharing the first one's NAME, so a differently named one went
// straight through — the count check has to run first.

func newCompanyRequest(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/companies", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	contextutil.SetUser(ctx, "employer-1")
	return ctx, recorder
}

// countResponseForCompanies builds the reply CountDocuments expects: an aggregate
// cursor whose single document holds the count under "n".
func countResponseForCompanies(n int32) bson.D {
	return mtest.CreateCursorResponse(1, "test.companies", mtest.FirstBatch, bson.D{
		{Key: "n", Value: n},
	})
}

func TestCreateCompanyRejectsASecondProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })

	// The employer already has one company, under a DIFFERENT name than the one
	// being submitted — the case the name-collision check could never catch.
	mt.AddMockResponses(countResponseForCompanies(1))

	ctx, recorder := newCompanyRequest(t,
		`{"name":"Totally Different Ltd","contact_person":"Ada","contact_person_phone":"+254700000000"}`)

	if err := HandleCreateCompany(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "already have a company profile") {
		t.Errorf("body should explain the limit, got %s", recorder.Body.String())
	}
}

func TestCreateCompanyAllowsTheFirstProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })

	mt.AddMockResponses(
		countResponseForCompanies(0),                                      // no company yet
		mtest.CreateCursorResponse(0, "test.companies", mtest.FirstBatch), // name lookup: none
		mtest.CreateSuccessResponse(),                                     // insert
	)

	ctx, recorder := newCompanyRequest(t,
		`{"name":"Acme Builders","contact_person":"Ada","contact_person_phone":"+254700000000"}`)

	if err := HandleCreateCompany(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
}

// Validation must still be what rejects a bad payload, so the limit check cannot
// mask a 400 as a 409.
func TestCreateCompanyStillValidatesBeforeCounting(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })

	ctx, recorder := newCompanyRequest(t, `{"name":"","contact_person":"","contact_person_phone":""}`)

	if err := HandleCreateCompany(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}
