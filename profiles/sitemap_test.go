package profiles

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/configs"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestHandleArtisanSitemap(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.example.com/")

	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("empty sitemap", func(mt *mtest.T) {
		useMTestDatabase(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mtest.TestDb+".users",
			mtest.FirstBatch,
		))

		recorder, err := runSitemapRequest()
		if err != nil {
			mt.Fatalf("HandleArtisanSitemap() error = %v", err)
		}
		assertSitemapResponseHeaders(mt, recorder)

		var urlSet sitemapURLSet
		if err := xml.Unmarshal(recorder.Body.Bytes(), &urlSet); err != nil {
			mt.Fatalf("decode sitemap XML: %v\nbody: %s", err, recorder.Body.String())
		}
		if urlSet.XMLNS != "http://www.sitemaps.org/schemas/sitemap/0.9" {
			mt.Errorf("xmlns = %q", urlSet.XMLNS)
		}
		if len(urlSet.URLs) != 0 {
			mt.Errorf("URLs = %#v, want empty", urlSet.URLs)
		}
		if !strings.HasPrefix(recorder.Body.String(), xml.Header) {
			mt.Errorf("body does not start with XML header: %q", recorder.Body.String())
		}
	})

	mt.Run("populated sitemap filters and formats records", func(mt *mtest.T) {
		useMTestDatabase(mt)
		updatedAt := time.Date(2026, time.August, 4, 23, 59, 59, 0, time.UTC)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mtest.TestDb+".users",
			mtest.FirstBatch,
			bson.D{
				{Key: "_id", Value: "artisan-1"},
				{Key: "slug", Value: "alice-kamau-plumber-nairobi"},
				{Key: "updated_at", Value: updatedAt},
			},
			bson.D{
				{Key: "_id", Value: "artisan-2"},
				{Key: "slug", Value: "bob-otieno-electrician-kisumu"},
			},
		))

		recorder, err := runSitemapRequest()
		if err != nil {
			mt.Fatalf("HandleArtisanSitemap() error = %v", err)
		}
		assertSitemapResponseHeaders(mt, recorder)

		var urlSet sitemapURLSet
		if err := xml.Unmarshal(recorder.Body.Bytes(), &urlSet); err != nil {
			mt.Fatalf("decode sitemap XML: %v\nbody: %s", err, recorder.Body.String())
		}
		wantURLs := []sitemapURL{
			{Loc: "https://api.example.com/artisan/alice-kamau-plumber-nairobi", LastMod: "2026-08-04"},
			{Loc: "https://api.example.com/artisan/bob-otieno-electrician-kisumu"},
		}
		if len(urlSet.URLs) != len(wantURLs) {
			mt.Fatalf("URL count = %d, want %d; URLs: %#v", len(urlSet.URLs), len(wantURLs), urlSet.URLs)
		}
		for index, want := range wantURLs {
			if got := urlSet.URLs[index]; got != want {
				mt.Errorf("URL[%d] = %#v, want %#v", index, got, want)
			}
		}
		if strings.Contains(recorder.Body.String(), "<lastmod></lastmod>") {
			mt.Errorf("zero updated_at emitted an empty lastmod element: %s", recorder.Body.String())
		}

		started := mt.GetStartedEvent()
		if started == nil || started.CommandName != "find" {
			mt.Fatalf("started command = %#v, want find", started)
		}
		assertSitemapFindCommand(mt, started.Command)
	})

	mt.Run("find error", func(mt *mtest.T) {
		useMTestDatabase(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    123,
			Message: "find failed",
		}))

		recorder, err := runSitemapRequest()
		if err != nil {
			mt.Fatalf("HandleArtisanSitemap() error = %v", err)
		}
		if recorder.Code != http.StatusInternalServerError {
			mt.Errorf("status = %d, want 500", recorder.Code)
		}
	})

	mt.Run("cursor decode error", func(mt *mtest.T) {
		useMTestDatabase(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mtest.TestDb+".users",
			mtest.FirstBatch,
			bson.D{
				{Key: "_id", Value: "artisan-1"},
				{Key: "slug", Value: "alice-kamau-plumber-nairobi"},
				{Key: "updated_at", Value: "not-a-date"},
			},
		))

		recorder, err := runSitemapRequest()
		if err != nil {
			mt.Fatalf("HandleArtisanSitemap() error = %v", err)
		}
		if recorder.Code != http.StatusInternalServerError {
			mt.Errorf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func useMTestDatabase(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{
		Client: mt.Client,
		DB:     mt.Client.Database(mtest.TestDb),
	}
	mt.Cleanup(func() { configs.MI = previous })
}

func runSitemapRequest() (*httptest.ResponseRecorder, error) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/sitemap-artisans.xml", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	return recorder, HandleArtisanSitemap(ctx)
}

func assertSitemapResponseHeaders(t testing.TB, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(echo.HeaderContentType); got != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, s-maxage=3600, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func assertSitemapFindCommand(t testing.TB, command bson.Raw) {
	t.Helper()

	var filter bson.M
	if err := bson.Unmarshal(command.Lookup("filter").Document(), &filter); err != nil {
		t.Fatalf("decode find filter: %v", err)
	}
	if got := filter["is_profile_public"]; got != true {
		t.Errorf("is_profile_public filter = %#v, want true", got)
	}
	if got := filter["user_type"]; got != "artisan" {
		t.Errorf("user_type filter = %#v, want artisan", got)
	}
	slugFilter, ok := filter["slug"].(bson.M)
	if !ok {
		t.Fatalf("slug filter = %#v, want document", filter["slug"])
	}
	if got := slugFilter["$ne"]; got != "" {
		t.Errorf("slug $ne filter = %#v, want empty string", got)
	}

	var projection bson.M
	if err := bson.Unmarshal(command.Lookup("projection").Document(), &projection); err != nil {
		t.Fatalf("decode find projection: %v", err)
	}
	for _, field := range []string{"slug", "updated_at"} {
		if got, ok := projection[field]; !ok || got != int32(1) {
			t.Errorf("projection[%q] = %#v, want int32(1)", field, got)
		}
	}
}
