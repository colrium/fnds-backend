package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/jobrequests"
	"backend/jwtutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

const validAdminVacancyJSON = `{
	"owner_id":"owner-1","company_id":"company-1","title":" Carpenter ",
	"level":"MID_LEVEL","positions":2,"employment_type":"FULL_TIME",
	"experience":"2 years","salary_minimum":20000,"salary_maximum":40000,
	"skill":"Carpenter","location":"Nakuru","description":"Build fitted cabinets",
	"responsibilities":"Measure and install","application_start_date":"2026-09-12",
	"application_end_date":"2026-10-12",
	"id":"injected","created_by":"someone-else","workspace_id":"admin-workspace",
	"company_name":"Forged company","status":"closed","is_featured":true
}`

func savedCompanyResponse() bson.D {
	return mtest.CreateCursorResponse(0, "test.companies", mtest.FirstBatch, bson.D{
		{Key: "_id", Value: "company-1"}, {Key: "owner_id", Value: "owner-1"},
		{Key: "workspace_id", Value: "owner-workspace"}, {Key: "name", Value: "Saved Builders"},
		{Key: "website", Value: "https://example.test"}, {Key: "location", Value: "Nakuru"},
		{Key: "contact_person", Value: "Alex"}, {Key: "contact_person_phone", Value: "+254700000000"},
		{Key: "contact_person_email", Value: "alex@example.test"},
	})
}

func TestAdminCreateVacancyUsesSelectedOwnerAndSavedCompany(t *testing.T) {
	mt := useAdminMockMongo(t, "job_vacancies")
	// Admin assistance is allowed for an existing account of any user type.
	queueUserLookup(mt, "owner-1", "artisan")
	mt.AddMockResponses(savedCompanyResponse(), mtest.CreateSuccessResponse())
	notifications := 0
	handler := vacancyCreationHandler{notify: func(_ context.Context, v jobrequests.JobVacancy, actor string) {
		notifications++
		if v.OwnerID != "owner-1" || actor != "admin-1" || v.ID == "" {
			t.Errorf("wrong notification owner/actor: %+v actor=%s", v, actor)
		}
	}}
	ctx, recorder := adminHandlerContext(http.MethodPost, "/vacancies", validAdminVacancyJSON)
	if err := handler.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct{ Data jobrequests.JobVacancy }
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	v := response.Data
	if v.OwnerID != "owner-1" || v.CreatedBy != "admin-1" || v.CompanyID != "company-1" || v.WorkspaceID != "owner-workspace" {
		t.Fatalf("wrong ownership: %+v", v)
	}
	if v.CompanyName != "Saved Builders" || v.ContactPerson != "Alex" || v.ContactPersonEmail != "alex@example.test" || v.CompanyWebsite != "https://example.test" {
		t.Fatalf("company snapshot not loaded from storage: %+v", v)
	}
	if v.ID == "" || v.ID == "injected" || v.Title != "Carpenter" || v.Status != "open" || v.IsFeatured || v.SalaryRange != "20000-40000" || v.ApplicationStartDate != "2026-09-12T00:00:00Z" {
		t.Fatalf("incorrect published vacancy: %+v", v)
	}
	if notifications != 1 {
		t.Fatalf("notification calls=%d", notifications)
	}
	var insertFound, companyScopeFound bool
	for _, event := range mt.GetAllStartedEvents() {
		if event.CommandName == "find" && event.Command.Lookup("find").StringValue() == "companies" {
			filter := event.Command.Lookup("filter").Document()
			companyScopeFound = filter.Lookup("_id").StringValue() == "company-1" &&
				filter.Lookup("owner_id").StringValue() == "owner-1"
		}
		if event.CommandName == "insert" {
			insertFound = event.Command.Lookup("insert").StringValue() == "job_vacancies"
		}
	}
	if !insertFound || !companyScopeFound {
		t.Fatalf("insertFound=%t companyScopeFound=%t", insertFound, companyScopeFound)
	}
}

func TestAdminCreateVacancyRejectsMissingOwnerCompanyAndStorageFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		responses []bson.D
		status    int
	}{
		{"deleted user", []bson.D{mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch)}, 404},
		{"user lookup failure", []bson.D{mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "lookup failed"})}, 500},
		{"missing or different owner's company", []bson.D{
			mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userLookupResponse("owner-1", "customer")),
			mtest.CreateCursorResponse(0, "test.companies", mtest.FirstBatch),
		}, 404},
		{"company lookup failure", []bson.D{
			mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userLookupResponse("owner-1", "customer")),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "lookup failed"}),
		}, 500},
		{"insert failure", []bson.D{
			mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userLookupResponse("owner-1", "customer")),
			savedCompanyResponse(),
			mtest.CreateWriteErrorsResponse(mtest.WriteError{Index: 0, Code: 2, Message: "write failed"}),
		}, 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			mt := useAdminMockMongo(t, "job_vacancies")
			mt.AddMockResponses(test.responses...)
			handler := vacancyCreationHandler{notify: func(context.Context, jobrequests.JobVacancy, string) {
				t.Error("failed creation must not notify")
			}}
			ctx, recorder := adminHandlerContext(http.MethodPost, "/vacancies", validAdminVacancyJSON)
			if err := handler.Create(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			for _, event := range mt.GetAllStartedEvents() {
				if event.CommandName == "insert" && test.name != "insert failure" {
					t.Error("invalid user or company caused a write")
				}
			}
		})
	}
}

func TestAdminCreateVacancyValidationBeforeDatabaseWrites(t *testing.T) {
	for _, test := range []struct {
		name, from, to string
	}{
		{"missing user", `"owner-1"`, `""`},
		{"no saved company", `"company-1"`, `""`},
		{"empty title", `" Carpenter "`, `"  "`},
		{"unknown skill", `"skill":"Carpenter"`, `"skill":"Not a trade"`},
		{"invalid level", `"MID_LEVEL"`, `"unknown"`},
		{"invalid employment", `"FULL_TIME"`, `"unknown"`},
		{"negative positions", `"positions":2`, `"positions":-1`},
		{"too many positions", `"positions":2`, `"positions":1001`},
		{"fractional positions", `"positions":2`, `"positions":1.5`},
		{"negative salary", `"salary_minimum":20000`, `"salary_minimum":-1`},
		{"reversed salary", `"salary_maximum":40000`, `"salary_maximum":10000`},
		{"bad date", `"2026-10-12"`, `"not-a-date"`},
		{"reversed dates", `"2026-10-12"`, `"2026-09-01"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mt := useAdminMockMongo(t, "job_vacancies")
			ctx, recorder := adminHandlerContext(http.MethodPost, "/vacancies", strings.Replace(validAdminVacancyJSON, test.from, test.to, 1))
			if err := (vacancyCreationHandler{}).Create(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusBadRequest || len(mt.GetAllStartedEvents()) != 0 {
				t.Fatalf("status=%d commands=%d body=%s", recorder.Code, len(mt.GetAllStartedEvents()), recorder.Body.String())
			}
		})
	}
}

const validAdminCompanyJSON = `{
	"name":" New Company ", "website":"https://example.test","location":"Nakuru",
	"contact_person":"Alex","contact_person_phone":"+254700000000","contact_person_email":"alex@example.test",
	"owner_id":"someone-else","workspace_id":"admin-workspace","created_by":"someone-else","id":"injected"
}`

func TestAdminCreateCompanyAllowsAdditionalProfileAndSetsOwnership(t *testing.T) {
	mt := useAdminMockMongo(t, "companies")
	queueUserLookup(mt, "owner-1", "customer")
	// Only the same-name lookup is required, so an owner's other profiles do
	// not prevent an admin from creating an additional company.
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.companies", mtest.FirstBatch), mtest.CreateSuccessResponse())
	ctx, recorder := adminHandlerContext(http.MethodPost, "/users/owner-1/companies", validAdminCompanyJSON)
	ctx.SetParamNames("id")
	ctx.SetParamValues("owner-1")
	if err := HandleCreateUserCompany(ctx); err != nil {
		t.Fatal(err)
	}
	var response struct{ Data jobrequests.Company }
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	company := response.Data
	if recorder.Code != 201 || company.OwnerID != "owner-1" || company.CreatedBy != "admin-1" || company.WorkspaceID != "" || company.ID == "injected" || company.Name != "New Company" {
		t.Fatalf("status=%d company=%+v body=%s", recorder.Code, company, recorder.Body.String())
	}
	for _, event := range mt.GetAllStartedEvents() {
		if event.CommandName == "aggregate" {
			t.Error("admin company creation must not apply the self-service company count limit")
		}
		if event.CommandName == "insert" && event.Command.Lookup("insert").StringValue() != "companies" {
			t.Error("company creation must not create or alter user accounts")
		}
	}
}

func TestAdminCreateCompanyRejectsMissingUserAndDuplicateName(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "missing user"
		if duplicate {
			name = "duplicate name"
		}
		t.Run(name, func(t *testing.T) {
			mt := useAdminMockMongo(t, "companies")
			status := 404
			if duplicate {
				queueUserLookup(mt, "owner-1", "customer")
				mt.AddMockResponses(savedCompanyResponse())
				status = 409
			} else {
				mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))
			}
			ctx, recorder := adminHandlerContext(http.MethodPost, "/users/owner-1/companies", validAdminCompanyJSON)
			ctx.SetParamNames("id")
			ctx.SetParamValues("owner-1")
			if err := HandleCreateUserCompany(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			for _, event := range mt.GetAllStartedEvents() {
				if event.CommandName == "insert" {
					t.Error("invalid creation wrote a document")
				}
			}
		})
	}
}

func TestAdminCreateCompanyValidatesBeforeWriting(t *testing.T) {
	for _, test := range []struct{ name, from, to string }{
		{"blank name", `" New Company "`, `" "`},
		{"blank contact", `"Alex"`, `" "`},
		{"blank phone", `"+254700000000"`, `" "`},
		{"invalid email", `"alex@example.test"`, `"not-an-email"`},
		{"script URL", `"https://example.test"`, `"javascript:alert(1)"`},
		{"URL without host", `"https://example.test"`, `"https:///missing-host"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mt := useAdminMockMongo(t, "companies")
			ctx, recorder := adminHandlerContext(http.MethodPost, "/users/owner-1/companies", strings.Replace(validAdminCompanyJSON, test.from, test.to, 1))
			ctx.SetParamNames("id")
			ctx.SetParamValues("owner-1")
			if err := HandleCreateUserCompany(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != 400 || len(mt.GetAllStartedEvents()) != 0 {
				t.Fatalf("status=%d commands=%d body=%s", recorder.Code, len(mt.GetAllStartedEvents()), recorder.Body.String())
			}
		})
	}
}
func TestAdminListCompanyProfilesScopesToSelectedUser(t *testing.T) {
	mt := useAdminMockMongo(t, "companies")
	queueUserLookup(mt, "owner-1", "customer")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.companies", mtest.FirstBatch))
	ctx, recorder := adminHandlerContext(http.MethodGet, "/users/owner-1/companies", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("owner-1")
	if err := HandleListUserCompanies(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"data":[]`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, event := range mt.GetAllStartedEvents() {
		if event.CommandName == "find" && event.Command.Lookup("find").StringValue() == "companies" {
			if event.Command.Lookup("filter").Document().Lookup("owner_id").StringValue() != "owner-1" {
				t.Fatal("company list is not scoped to the selected owner")
			}
		}
	}
}

func TestAdminVacancyCreationRoutesRejectNonAdmins(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/admin/vacancies/options"},
		{http.MethodPost, "/admin/vacancies"},
		{http.MethodGet, "/admin/users/owner-1/companies"},
		{http.MethodPost, "/admin/users/owner-1/companies"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			mt := useAdminMockMongo(t, "users")
			queueUserLookup(mt, "customer-1", "customer")
			queueUserLookup(mt, "customer-1", "customer")
			token, _, err := jwtutil.GenerateToken("customer-1")
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(validAdminVacancyJSON))
			request.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
			request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			recorder := httptest.NewRecorder()
			buildAdminGroup().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
