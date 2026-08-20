package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"backend/reviews"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

type publicProfileContextKey struct{}

func TestHandleGetPublicArtisanProfileProductionWrapperRejectsMissingSlug(t *testing.T) {
	app := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artisans/", nil)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	ctx.SetParamNames("slug")
	ctx.SetParamValues("")

	if err := HandleGetPublicArtisanProfile(ctx); err != nil {
		t.Fatalf("HandleGetPublicArtisanProfile() error = %v", err)
	}
	assertPublicProfileError(t, recorder, http.StatusBadRequest, "Invalid or missing slug")
}

func TestHandleGetPublicArtisanProfile_InvalidIdentifier(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{name: "missing slug", slug: ""},
		{name: "malformed slug", slug: "Not/A/Slug"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := publicArtisanProfileDependencies{}
			if test.slug != "" {
				deps.getPublicUserByFundisID = func(context.Context, string) (*users.User, error) {
					return nil, mongo.ErrNoDocuments
				}
			}

			recorder, err := runPublicArtisanProfileRequest(test.slug, context.Background(), deps)
			if err != nil {
				t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
			}
			assertPublicProfileError(t, recorder, http.StatusBadRequest, "Invalid or missing slug")
		})
	}
}

func TestHandleGetPublicArtisanProfile_RedirectsFundisID(t *testing.T) {
	requestCtx := context.WithValue(context.Background(), publicProfileContextKey{}, "request-context")
	deps := publicArtisanProfileDependencies{
		getPublicUserByFundisID: func(ctx context.Context, identifier string) (*users.User, error) {
			assertPublicProfileRequestContext(t, ctx)
			if identifier != "FND-00123" {
				t.Errorf("identifier = %q, want FND-00123", identifier)
			}
			return &users.User{Slug: "alice-kamau-painter-nakuru"}, nil
		},
	}

	recorder, err := runPublicArtisanProfileRequest("FND-00123", requestCtx, deps)
	if err != nil {
		t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
	}
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	if got, want := recorder.Header().Get("Location"), "/api/v1/artisans/alice-kamau-painter-nakuru"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleGetPublicArtisanProfile_UnknownSlug(t *testing.T) {
	requestCtx := context.WithValue(context.Background(), publicProfileContextKey{}, "request-context")
	deps := publicArtisanProfileDependencies{
		getUserBySlug: func(ctx context.Context, slug string) (*users.User, error) {
			assertPublicProfileRequestContext(t, ctx)
			if slug != "unknown-artisan" {
				t.Errorf("slug = %q, want unknown-artisan", slug)
			}
			return nil, mongo.ErrNoDocuments
		},
	}

	recorder, err := runPublicArtisanProfileRequest("unknown-artisan", requestCtx, deps)
	if err != nil {
		t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
	}
	assertPublicProfileError(t, recorder, http.StatusNotFound, "Artisan not found")
}

func TestHandleGetPublicArtisanProfile_AllowsMissingOptionalProfile(t *testing.T) {
	deps := successfulPublicArtisanProfileDependencies(t)
	deps.getProfileByUserID = func(context.Context, string) (*Profile, error) {
		return nil, mongo.ErrNoDocuments
	}
	deps.getSkillNamesByIDs = func(context.Context, []string) ([]string, error) {
		t.Fatal("getSkillNamesByIDs() called for a missing optional profile")
		return nil, nil
	}
	deps.getLocationNamesByIDs = func(context.Context, []string) ([]string, error) {
		t.Fatal("getLocationNamesByIDs() called for a missing optional profile")
		return nil, nil
	}

	recorder, err := runPublicArtisanProfileRequest("alice-kamau-painter-nakuru", context.Background(), deps)
	if err != nil {
		t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Success bool                 `json:"success"`
		Data    PublicArtisanProfile `json:"data"`
		Message string               `json:"message"`
	}
	decodePublicProfileResponse(t, recorder, &response)
	if !response.Success {
		t.Error("success = false, want true")
	}
	if response.Data.BusinessName != "" || response.Data.City != "" {
		t.Errorf("optional profile fields = (%q, %q), want empty", response.Data.BusinessName, response.Data.City)
	}
	if response.Data.Skills == nil || len(response.Data.Skills) != 0 {
		t.Errorf("skills = %#v, want []", response.Data.Skills)
	}
	if response.Data.Locations == nil || len(response.Data.Locations) != 0 {
		t.Errorf("locations = %#v, want []", response.Data.Locations)
	}
}

func TestHandleGetPublicArtisanProfile_DependencyErrors(t *testing.T) {
	dependencyError := errors.New("dependency unavailable")
	tests := []struct {
		name string
		fail func(*publicArtisanProfileDependencies)
	}{
		{
			name: "profile",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getProfileByUserID = func(context.Context, string) (*Profile, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "skills",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getSkillNamesByIDs = func(context.Context, []string) ([]string, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "locations",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getLocationNamesByIDs = func(context.Context, []string) ([]string, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "reviews",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.listReviews = func(context.Context, bson.M) ([]reviews.Review, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "working hours",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getWorkingHours = func(context.Context, string) ([]WorkingHourPublic, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "languages",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getLanguages = func(context.Context, string) ([]LanguagePublic, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "past employers",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.getPastEmployers = func(context.Context, string) ([]PastEmployerPublic, error) {
					return nil, dependencyError
				}
			},
		},
		{
			name: "completed jobs",
			fail: func(deps *publicArtisanProfileDependencies) {
				deps.countCompletedJobs = func(context.Context, string) (int, error) {
					return 0, dependencyError
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := successfulPublicArtisanProfileDependencies(t)
			test.fail(&deps)

			recorder, err := runPublicArtisanProfileRequest("alice-kamau-painter-nakuru", context.Background(), deps)
			if err != nil {
				t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
			}
			assertPublicProfileError(t, recorder, http.StatusInternalServerError, "Failed to retrieve artisan profile")
		})
	}
}

func TestHandleGetPublicArtisanProfile_SuccessEnvelopeCacheAndContext(t *testing.T) {
	requestCtx := context.WithValue(context.Background(), publicProfileContextKey{}, "request-context")
	user := &users.User{
		ID:          "artisan-123",
		FundisID:    "FND-00123",
		Slug:        "alice-kamau-painter-nakuru",
		FirstName:   "Alice",
		LastName:    "Kamau",
		IsAvailable: true,
		IsVerified:  true,
	}
	profile := &Profile{
		BusinessName:      "Kamau Painters",
		City:              "Nakuru",
		County:            "Nakuru",
		Country:           "Kenya",
		YearsOfExperience: 8,
		Skills:            []string{"skill-1"},
		Locations:         []string{"location-1"},
	}

	deps := publicArtisanProfileDependencies{
		getUserBySlug: func(ctx context.Context, slug string) (*users.User, error) {
			assertPublicProfileRequestContext(t, ctx)
			if slug != user.Slug {
				t.Errorf("slug = %q, want %q", slug, user.Slug)
			}
			return user, nil
		},
		getProfileByUserID: func(ctx context.Context, userID string) (*Profile, error) {
			assertPublicProfileDependencyCall(t, ctx, userID)
			return profile, nil
		},
		getSkillNamesByIDs: func(ctx context.Context, ids []string) ([]string, error) {
			assertPublicProfileRequestContext(t, ctx)
			if !reflect.DeepEqual(ids, profile.Skills) {
				t.Errorf("skill IDs = %v, want %v", ids, profile.Skills)
			}
			return []string{"Painting"}, nil
		},
		getLocationNamesByIDs: func(ctx context.Context, ids []string) ([]string, error) {
			assertPublicProfileRequestContext(t, ctx)
			if !reflect.DeepEqual(ids, profile.Locations) {
				t.Errorf("location IDs = %v, want %v", ids, profile.Locations)
			}
			return []string{"Nakuru"}, nil
		},
		listReviews: func(ctx context.Context, filter bson.M) ([]reviews.Review, error) {
			assertPublicProfileRequestContext(t, ctx)
			if !reflect.DeepEqual(filter, bson.M{"artisan_id": user.ID}) {
				t.Errorf("review filter = %#v, want artisan_id %q", filter, user.ID)
			}
			return []reviews.Review{{Rating: 5}, {Rating: 4}}, nil
		},
		getWorkingHours: func(ctx context.Context, userID string) ([]WorkingHourPublic, error) {
			assertPublicProfileDependencyCall(t, ctx, userID)
			return []WorkingHourPublic{{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"}}, nil
		},
		getLanguages: func(ctx context.Context, userID string) ([]LanguagePublic, error) {
			assertPublicProfileDependencyCall(t, ctx, userID)
			return []LanguagePublic{{Language: "English", ProficiencyLevel: "fluent"}}, nil
		},
		getPastEmployers: func(ctx context.Context, userID string) ([]PastEmployerPublic, error) {
			assertPublicProfileDependencyCall(t, ctx, userID)
			return []PastEmployerPublic{{Name: "Grid Works", Location: "Nairobi"}}, nil
		},
		countCompletedJobs: func(ctx context.Context, userID string) (int, error) {
			assertPublicProfileDependencyCall(t, ctx, userID)
			return 12, nil
		},
	}

	recorder, err := runPublicArtisanProfileRequest(user.Slug, requestCtx, deps)
	if err != nil {
		t.Fatalf("handleGetPublicArtisanProfile() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if got, want := recorder.Header().Get("Cache-Control"), "public, s-maxage=300, max-age=60"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
	if got := recorder.Header().Get(echo.HeaderContentType); got != echo.MIMEApplicationJSON {
		t.Errorf("Content-Type = %q, want %q", got, echo.MIMEApplicationJSON)
	}

	var response struct {
		Success bool                 `json:"success"`
		Data    PublicArtisanProfile `json:"data"`
		Message string               `json:"message"`
	}
	decodePublicProfileResponse(t, recorder, &response)
	if !response.Success {
		t.Error("success = false, want true")
	}
	if response.Message != "Artisan profile retrieved successfully" {
		t.Errorf("message = %q, want success message", response.Message)
	}
	want := PublicArtisanProfile{
		FundisID:           "FND-00123",
		Slug:               "alice-kamau-painter-nakuru",
		FirstName:          "Alice",
		LastName:           "Kamau",
		BusinessName:       "Kamau Painters",
		City:               "Nakuru",
		County:             "Nakuru",
		Country:            "Kenya",
		YearsOfExperience:  8,
		Skills:             []string{"Painting"},
		Locations:          []string{"Nakuru"},
		IsAvailable:        true,
		IsVerified:         true,
		Rating:             4.5,
		ReviewCount:        2,
		CompletedJobsCount: 12,
		WorkingHours:       []WorkingHourPublic{{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"}},
		Languages:          []LanguagePublic{{Language: "English", ProficiencyLevel: "fluent"}},
		PastEmployers:      []PastEmployerPublic{{Name: "Grid Works", Location: "Nairobi"}},
	}
	if !reflect.DeepEqual(response.Data, want) {
		t.Errorf("data mismatch\n got: %#v\nwant: %#v", response.Data, want)
	}
}

func successfulPublicArtisanProfileDependencies(t *testing.T) publicArtisanProfileDependencies {
	t.Helper()
	return publicArtisanProfileDependencies{
		getUserBySlug: func(context.Context, string) (*users.User, error) {
			return &users.User{
				ID:        "artisan-123",
				FundisID:  "FND-00123",
				Slug:      "alice-kamau-painter-nakuru",
				FirstName: "Alice",
				LastName:  "Kamau",
			}, nil
		},
		getProfileByUserID: func(context.Context, string) (*Profile, error) {
			return &Profile{Skills: []string{"skill-1"}, Locations: []string{"location-1"}}, nil
		},
		getSkillNamesByIDs: func(context.Context, []string) ([]string, error) {
			return []string{"Painting"}, nil
		},
		getLocationNamesByIDs: func(context.Context, []string) ([]string, error) {
			return []string{"Nakuru"}, nil
		},
		listReviews: func(context.Context, bson.M) ([]reviews.Review, error) {
			return []reviews.Review{}, nil
		},
		getWorkingHours: func(context.Context, string) ([]WorkingHourPublic, error) {
			return []WorkingHourPublic{}, nil
		},
		getLanguages: func(context.Context, string) ([]LanguagePublic, error) {
			return []LanguagePublic{}, nil
		},
		getPastEmployers: func(context.Context, string) ([]PastEmployerPublic, error) {
			return []PastEmployerPublic{}, nil
		},
		countCompletedJobs: func(context.Context, string) (int, error) {
			return 0, nil
		},
	}
}

func runPublicArtisanProfileRequest(slug string, requestCtx context.Context, deps publicArtisanProfileDependencies) (*httptest.ResponseRecorder, error) {
	app := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artisans/"+slug, nil).WithContext(requestCtx)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	ctx.SetPath("/api/v1/artisans/:slug")
	ctx.SetParamNames("slug")
	ctx.SetParamValues(slug)
	return recorder, handleGetPublicArtisanProfile(ctx, deps)
}

func assertPublicProfileError(t *testing.T, recorder *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, status, recorder.Body.String())
	}
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	decodePublicProfileResponse(t, recorder, &response)
	if response.Success {
		t.Error("success = true, want false")
	}
	if response.Message != message {
		t.Errorf("message = %q, want %q", response.Message, message)
	}
}

func decodePublicProfileResponse(t *testing.T, recorder *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}

func assertPublicProfileRequestContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if got := ctx.Value(publicProfileContextKey{}); got != "request-context" {
		t.Errorf("request context value = %#v, want request-context", got)
	}
}

func assertPublicProfileDependencyCall(t *testing.T, ctx context.Context, userID string) {
	t.Helper()
	assertPublicProfileRequestContext(t, ctx)
	if userID != "artisan-123" {
		t.Errorf("user ID = %q, want artisan-123", userID)
	}
}

func TestHandleGetPublicArtisanProfileProductionWrapperValidRequest(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("uses production dependencies", func(mt *mtest.T) {
		useMockMongo(mt)
		namespace := func(collection string) string { return mt.DB.Name() + "." + collection }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, namespace("users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "artisan-123"},
				{Key: "fundis_id", Value: "FND-00123"},
				{Key: "slug", Value: "alice-kamau-painter-nakuru"},
				{Key: "first_name", Value: "Alice"},
				{Key: "last_name", Value: "Kamau"},
				{Key: "is_profile_public", Value: true},
			}),
			mtest.CreateCursorResponse(0, namespace("profiles"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, namespace("reviews"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, namespace("working_hours"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, namespace("language_proficiencies"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, namespace("past_employers"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, namespace("job_requests"), mtest.FirstBatch,
				bson.D{{Key: "n", Value: int32(0)}}),
		)

		app := echo.New()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/artisans/alice-kamau-painter-nakuru", nil)
		recorder := httptest.NewRecorder()
		ctx := app.NewContext(request, recorder)
		ctx.SetParamNames("slug")
		ctx.SetParamValues("alice-kamau-painter-nakuru")

		if err := HandleGetPublicArtisanProfile(ctx); err != nil {
			mt.Fatalf("HandleGetPublicArtisanProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Success bool                 `json:"success"`
			Data    PublicArtisanProfile `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			mt.Fatalf("decode response: %v", err)
		}
		if !response.Success || response.Data.FundisID != "FND-00123" {
			mt.Errorf("response = %#v, want successful production-wired profile", response)
		}
	})
}
