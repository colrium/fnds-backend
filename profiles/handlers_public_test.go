package profiles

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/configs"
	"backend/contextutil"

	"github.com/alicebob/miniredis/v2"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestContainsUUID(t *testing.T) {
	tests := []struct {
		name string
		slug string
		want bool
	}{
		{name: "embedded lowercase UUID", slug: "alice-123e4567-e89b-12d3-a456-426614174000-nairobi", want: true},
		{name: "normal slug", slug: "alice-kamau-plumber-nairobi"},
		{name: "uppercase is not a stored UUID slug", slug: "123E4567-E89B-12D3-A456-426614174000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsUUID(test.slug); got != test.want {
				t.Errorf("containsUUID(%q) = %v, want %v", test.slug, got, test.want)
			}
		})
	}
}

func TestHandleTogglePublicProfileGuards(t *testing.T) {
	recorder, err := runTogglePublicProfileRequest("", `{"is_profile_public":true}`)
	if err != nil {
		t.Fatalf("HandleTogglePublicProfile() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized status = %d, want 401", recorder.Code)
	}

	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("missing user", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "find failed"}))

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusNotFound {
			mt.Errorf("status = %d, want 404", recorder.Code)
		}
	})
	mt.Run("non-artisan", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(userCursorResponse(mt, bson.D{
			{Key: "_id", Value: "user-1"},
			{Key: "user_type", Value: "customer"},
		}))

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusForbidden {
			mt.Errorf("status = %d, want 403", recorder.Code)
		}
	})
	mt.Run("invalid JSON", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(userCursorResponse(mt, artisanDocument("existing-slug")))

		recorder, err := runTogglePublicProfileRequest("user-1", `{`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusBadRequest {
			mt.Errorf("status = %d, want 400", recorder.Code)
		}
	})
}

func TestHandleTogglePublicProfilePrivateInvalidatesProfileAndSitemap(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("private", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":false}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		assertPublicProfileCachesDeleted(mt, server, slug)
	})
}

func TestHandleTogglePublicProfilePublishesExistingSlugAndInvalidatesCaches(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("publish existing slug", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"slug":"`+slug+`"`) {
			mt.Errorf("response body does not contain published slug: %s", recorder.Body.String())
		}
		assertPublicProfileCachesDeleted(mt, server, slug)
	})
}

func TestHandleTogglePublicProfileGeneratesHumanReadableSlug(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("generate slug", func(mt *mtest.T) {
		useMockMongo(mt)
		_ = useProfileTestRedis(mt.T)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("")),
			mtest.CreateCursorResponse(
				0,
				mt.DB.Name()+".profiles",
				mtest.FirstBatch,
				bson.D{
					{Key: "_id", Value: "profile-1"},
					{Key: "user_id", Value: "user-1"},
					{Key: "skills", Value: bson.A{"skill-1"}},
					{Key: "city", Value: "Nairobi"},
				},
			),
			mtest.CreateCursorResponse(
				0,
				mt.DB.Name()+".skills",
				mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "skill-1"}, {Key: "name", Value: "Plumber"}},
			),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"slug":"alice-kamau-plumber-nairobi"`) {
			mt.Errorf("response body does not contain generated human-readable slug: %s", recorder.Body.String())
		}
	})
}

func runTogglePublicProfileRequest(userID, body string) (*httptest.ResponseRecorder, error) {
	app := echo.New()
	request := httptest.NewRequest(http.MethodPatch, "/profile/public", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return recorder, HandleTogglePublicProfile(ctx)
}

func userCursorResponse(mt *mtest.T, document bson.D) bson.D {
	return mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch, document)
}

func artisanDocument(slug string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "first_name", Value: "Alice"},
		{Key: "last_name", Value: "Kamau"},
		{Key: "user_type", Value: "artisan"},
		{Key: "slug", Value: slug},
	}
}

func useProfileTestRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	previous := configs.RDB
	configs.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		configs.RDB = previous
	})
	return server
}

func seedPublicProfileCaches(t testing.TB, server *miniredis.Miniredis, slug string) {
	t.Helper()
	mustSetProfileCache(t, server, "artisan-profile:/api/v1/artisans/"+slug)
	mustSetProfileCache(t, server, "artisan-share:/artisan/"+slug)
	mustSetProfileCache(t, server, "artisan-sitemap:/sitemap-artisans.xml")
}

func mustSetProfileCache(t testing.TB, server *miniredis.Miniredis, key string) {
	t.Helper()
	if err := server.Set(key, "cached"); err != nil {
		t.Fatalf("set Redis key %q: %v", key, err)
	}
}

func assertPublicProfileCachesDeleted(t testing.TB, server *miniredis.Miniredis, slug string) {
	t.Helper()
	for _, key := range []string{
		"artisan-profile:/api/v1/artisans/" + slug,
		"artisan-share:/artisan/" + slug,
		"artisan-sitemap:/sitemap-artisans.xml",
	} {
		if server.Exists(key) {
			t.Errorf("cache key %q was not invalidated", key)
		}
	}
}

func TestHandleUpdateProfileInvalidatesPublicProfileCaches(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("successful update", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mustSetProfileCache(mt.T, server, "artisan-profile:/api/v1/artisans/"+slug+"?view=compact")
		mustSetProfileCache(mt.T, server, "artisan-share:/artisan/"+slug+"?ref=profile")
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			userCursorResponse(mt, artisanDocument(slug)),
		)

		recorder, err := runUpdateProfileRequest("user-1", `{}`)
		if err != nil {
			mt.Fatalf("HandleUpdateProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		for _, key := range []string{
			"artisan-profile:/api/v1/artisans/" + slug,
			"artisan-share:/artisan/" + slug,
		} {
			if server.Exists(key) {
				mt.Errorf("cache key %q was not invalidated", key)
			}
		}
		for _, key := range []string{
			"artisan-profile:/api/v1/artisans/" + slug + "?view=compact",
			"artisan-share:/artisan/" + slug + "?ref=profile",
		} {
			if !server.Exists(key) {
				mt.Errorf("query cache key %q should remain for TTL eviction", key)
			}
		}
		if !server.Exists("artisan-sitemap:/sitemap-artisans.xml") {
			mt.Error("profile update unexpectedly invalidated the artisan sitemap")
		}
	})
}

func TestHandleUpdateProfilePostUpdateFailuresAreNonFatal(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("user lookup failure", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "find failed"}),
		)

		recorder, err := runUpdateProfileRequest("user-1", `{}`)
		if err != nil {
			mt.Fatalf("HandleUpdateProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !server.Exists("artisan-profile:/api/v1/artisans/" + slug) {
			mt.Error("cache should remain when the post-update user lookup fails")
		}
	})

	mt.Run("cache failure", func(mt *mtest.T) {
		useMockMongo(mt)
		previous := configs.RDB
		configs.RDB = nil
		mt.Cleanup(func() { configs.RDB = previous })
		const slug = "alice-kamau-plumber-nairobi"
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			userCursorResponse(mt, artisanDocument(slug)),
		)

		recorder, err := runUpdateProfileRequest("user-1", `{}`)
		if err != nil {
			mt.Fatalf("HandleUpdateProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestHandleUpdateProfileValidationFailureDoesNotInvalidateCache(t *testing.T) {
	server := useProfileTestRedis(t)
	const slug = "alice-kamau-plumber-nairobi"
	seedPublicProfileCaches(t, server, slug)

	recorder, err := runUpdateProfileRequest("user-1", `{"skills":["skill-1","skill-2"]}`)
	if err != nil {
		t.Fatalf("HandleUpdateProfile() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body.String())
	}
	if !server.Exists("artisan-profile:/api/v1/artisans/" + slug) {
		t.Error("validation failure unexpectedly invalidated the profile cache")
	}
}

func artisanDocumentWithProfileStatus(slug, profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "first_name", Value: "Alice"},
		{Key: "last_name", Value: "Kamau"},
		{Key: "user_type", Value: "artisan"},
		{Key: "slug", Value: slug},
		{Key: "profile_status", Value: profileStatus},
	}
}

func TestHandleUpdateProfileRevertsApprovedStatusToPendingReview(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("approved reverts on edit", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocumentWithProfileStatus(slug, "approved")),
			userCursorResponse(mt, artisanDocumentWithProfileStatus(slug, "approved")),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
		)

		recorder, err := runUpdateProfileRequest("user-1", `{}`)
		if err != nil {
			mt.Fatalf("HandleUpdateProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		events := mt.GetAllStartedEvents()
		if len(events) == 0 {
			mt.Fatal("no MongoDB commands were recorded")
		}
		lastEvent := events[len(events)-1]
		if lastEvent.CommandName != "update" {
			mt.Fatalf("last started event = %#v, want the profile_status revert update", lastEvent)
		}
	})
}

func runUpdateProfileRequest(userID, body string) (*httptest.ResponseRecorder, error) {
	app := echo.New()
	request := httptest.NewRequest(http.MethodPatch, "/profile", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return recorder, HandleUpdateProfile(ctx)
}

func TestHandleTogglePublicProfileFailureAndFallbackPaths(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("private update failure", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("alice-kamau-plumber-nairobi")),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "update failed"}),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":false}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusInternalServerError {
			mt.Errorf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
		}
	})

	mt.Run("missing profile uses name-only slug", func(mt *mtest.T) {
		useMockMongo(mt)
		previous := configs.RDB
		configs.RDB = nil
		mt.Cleanup(func() { configs.RDB = previous })
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("")),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".profiles", mtest.FirstBatch),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"slug":"alice-kamau"`) {
			mt.Errorf("response body does not contain name-only slug: %s", recorder.Body.String())
		}
	})

	mt.Run("profile lookup failure", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("")),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "profile lookup failed"}),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "profile unavailable") {
			mt.Errorf("response = (%d, %s), want 500 profile unavailable", recorder.Code, recorder.Body.String())
		}
	})

	mt.Run("skill lookup failure falls back to name and city slug", func(mt *mtest.T) {
		useMockMongo(mt)
		previous := configs.RDB
		configs.RDB = nil
		mt.Cleanup(func() { configs.RDB = previous })
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("")),
			mtest.CreateCursorResponse(
				0,
				mt.DB.Name()+".profiles",
				mtest.FirstBatch,
				bson.D{
					{Key: "_id", Value: "profile-1"},
					{Key: "user_id", Value: "user-1"},
					{Key: "skills", Value: bson.A{"skill-1"}},
					{Key: "city", Value: "Nairobi"},
				},
			),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "skill lookup failed"}),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"slug":"alice-kamau-nairobi"`) {
			mt.Errorf("response body does not contain fallback slug: %s", recorder.Body.String())
		}
	})

	mt.Run("publication failure", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument("alice-kamau-plumber-nairobi")),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "publish failed"}),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "Failed to update profile visibility") {
			mt.Errorf("response = (%d, %s), want publication failure", recorder.Code, recorder.Body.String())
		}
	})

	mt.Run("Redis unavailable invalidation is nonfatal", func(mt *mtest.T) {
		useMockMongo(mt)
		previous := configs.RDB
		configs.RDB = nil
		mt.Cleanup(func() { configs.RDB = previous })
		const slug = "alice-kamau-plumber-nairobi"
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocument(slug)),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		recorder, err := runTogglePublicProfileRequest("user-1", `{"is_profile_public":true}`)
		if err != nil {
			mt.Fatalf("HandleTogglePublicProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"slug":"`+slug+`"`) {
			mt.Errorf("response = (%d, %s), want successful publish despite cache failures", recorder.Code, recorder.Body.String())
		}
	})
}
