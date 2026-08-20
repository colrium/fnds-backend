package adminapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"backend/jobrequests"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestBuildVacancyFilterAppliesStatus(t *testing.T) {
	got := buildVacancyFilter("open")
	want := bson.M{"status": "open"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildVacancyFilter(\"open\") = %#v, want %#v", got, want)
	}
}

func TestBuildVacancyFilterEmptyStatusReturnsEmptyFilter(t *testing.T) {
	got := buildVacancyFilter("")
	if len(got) != 0 {
		t.Errorf("buildVacancyFilter(\"\") = %#v, want empty", got)
	}
}

func TestHandleListVacanciesReturnsPagedEnvelope(t *testing.T) {
	mt := useAdminMockMongo(t, "job_vacancies")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.job_vacancies",
		mtest.FirstBatch,
		bson.D{
			{Key: "_id", Value: "vac-1"},
			{Key: "title", Value: "Senior Plumber"},
			{Key: "status", Value: "open"},
			{Key: "owner_id", Value: "owner-1"},
			{Key: "company_id", Value: "comp-1"},
		},
	))
	// CountDocuments mock response
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.job_vacancies",
		mtest.FirstBatch,
		bson.D{{Key: "n", Value: 1}},
	))

	ctx, rec := adminHandlerContext(http.MethodGet, "/vacancies", "")
	if err := HandleListVacancies(ctx); err != nil {
		t.Fatalf("HandleListVacancies() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Data  []jobrequests.JobVacancy `json:"data"`
			Total int64                    `json:"total"`
			Page  int64                    `json:"page"`
			Limit int64                    `json:"limit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Data.Data) != 1 || body.Data.Data[0].ID != "vac-1" {
		t.Errorf("response data = %#v", body.Data.Data)
	}
	if body.Data.Total != 1 {
		t.Errorf("total = %d, want 1", body.Data.Total)
	}
}

func TestHandleGetVacancyNotFoundReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "job_vacancies")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.job_vacancies", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodGet, "/vacancies/missing", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleGetVacancy(ctx); err != nil {
		t.Fatalf("HandleGetVacancy() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetVacancyReturnsVacancy(t *testing.T) {
	mt := useAdminMockMongo(t, "job_vacancies")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.job_vacancies",
		mtest.FirstBatch,
		bson.D{
			{Key: "_id", Value: "vac-1"},
			{Key: "title", Value: "Senior Plumber"},
			{Key: "status", Value: "open"},
			{Key: "owner_id", Value: "owner-1"},
			{Key: "company_id", Value: "comp-1"},
		},
	))

	ctx, rec := adminHandlerContext(http.MethodGet, "/vacancies/vac-1", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("vac-1")

	if err := HandleGetVacancy(ctx); err != nil {
		t.Fatalf("HandleGetVacancy() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "vac-1") {
		t.Errorf("body missing vacancy ID: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Senior Plumber") {
		t.Errorf("body missing vacancy title: %s", rec.Body.String())
	}
}
