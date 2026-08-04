package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/reviews"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestHandleArtisanSharePageProductionWrapperRejectsMissingSlug(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/artisan/", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("slug")
	ctx.SetParamValues("")

	if err := HandleArtisanSharePage(ctx); err != nil {
		t.Fatalf("HandleArtisanSharePage() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Invalid artisan link") {
		t.Errorf("body = %q, want invalid-link message", recorder.Body.String())
	}
}

func TestBuildArtisanJSONLD(t *testing.T) {
	tests := []struct {
		name            string
		rating          float64
		reviewCount     int
		wantAggregate   bool
		wantRatingValue string
	}{
		{
			name:          "without ratings",
			wantAggregate: false,
		},
		{
			name:            "with ratings",
			rating:          4.25,
			reviewCount:     2,
			wantAggregate:   true,
			wantRatingValue: "4.2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := buildArtisanJSONLD(
				`Alice </script><script>alert("x")</script>`,
				"Plumbing & Heating",
				"Nairobi <CBD>",
				test.rating,
				test.reviewCount,
				"https://api.example.com/artisan/alice",
			)

			if strings.Contains(result, "</script>") || strings.Contains(result, "<CBD>") {
				t.Fatalf("buildArtisanJSONLD() emitted unescaped script/HTML content: %s", result)
			}

			var document map[string]any
			if err := json.Unmarshal([]byte(result), &document); err != nil {
				t.Fatalf("buildArtisanJSONLD() returned invalid JSON: %v", err)
			}
			if got := document["name"]; got != `Alice </script><script>alert("x")</script> - Plumbing & Heating` {
				t.Errorf("name = %q, want original content after JSON decoding", got)
			}
			aggregate, found := document["aggregateRating"]
			if found != test.wantAggregate {
				t.Fatalf("aggregateRating present = %v, want %v", found, test.wantAggregate)
			}
			if test.wantAggregate {
				rating := aggregate.(map[string]any)
				if got := rating["ratingValue"]; got != test.wantRatingValue {
					t.Errorf("ratingValue = %q, want %q", got, test.wantRatingValue)
				}
				if got := int(rating["reviewCount"].(float64)); got != test.reviewCount {
					t.Errorf("reviewCount = %d, want %d", got, test.reviewCount)
				}
			}
		})
	}
}

func TestHandleArtisanSharePageBranches(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.example.com/")
	t.Setenv("APP_PUBLIC_URL", "https://app.example.com/")

	errDependency := errors.New("profile dependency failed")
	validSlug := "alice-kamau-plumber-nairobi"

	tests := []struct {
		name           string
		slug           string
		configure      func(*artisanShareDependencies)
		wantStatus     int
		wantLocation   string
		wantBody       []string
		wantBodyAbsent []string
	}{
		{
			name:       "invalid link",
			slug:       "not a valid slug!",
			wantStatus: http.StatusBadRequest,
			wantBody:   []string{"Invalid artisan link", "profile link is invalid"},
		},
		{
			name: "fundis ID redirects to canonical slug",
			slug: "FND-001",
			configure: func(deps *artisanShareDependencies) {
				deps.getPublicUserByFundisID = func(_ context.Context, fundisID string) (*users.User, error) {
					if fundisID != "FND-001" {
						t.Fatalf("fundis ID = %q, want FND-001", fundisID)
					}
					return &users.User{Slug: validSlug}, nil
				}
			},
			wantStatus:   http.StatusFound,
			wantLocation: "/artisan/" + validSlug,
		},
		{
			name: "user not found",
			slug: validSlug,
			configure: func(deps *artisanShareDependencies) {
				deps.getUserBySlug = func(context.Context, string) (*users.User, error) {
					return nil, mongo.ErrNoDocuments
				}
			},
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"Artisan not found", "profile could not be found"},
		},
		{
			name: "profile missing renders with user data only",
			slug: validSlug,
			configure: func(deps *artisanShareDependencies) {
				deps.getProfileByUserID = func(context.Context, string) (*Profile, error) {
					return nil, mongo.ErrNoDocuments
				}
			},
			wantStatus: http.StatusOK,
			wantBody:   []string{"Alice", "Professional in Kenya"},
		},
		{
			name: "profile dependency error",
			slug: validSlug,
			configure: func(deps *artisanShareDependencies) {
				deps.getProfileByUserID = func(context.Context, string) (*Profile, error) {
					return nil, errDependency
				}
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   []string{"<title>Fundis</title>", "Something went wrong"},
		},
		{
			name: "reviews dependency error",
			slug: validSlug,
			configure: func(deps *artisanShareDependencies) {
				deps.listReviews = func(context.Context, bson.M) ([]reviews.Review, error) {
					return nil, errDependency
				}
			},
			wantStatus:     http.StatusInternalServerError,
			wantBody:       []string{"<title>Fundis</title>", "Something went wrong"},
			wantBodyAbsent: []string{errDependency.Error()},
		},
		{
			name:       "defaults render when optional data is empty",
			slug:       validSlug,
			wantStatus: http.StatusOK,
			wantBody: []string{
				"Professional in Kenya",
				"Experience:</span> N/A",
				"Rating:</span> No ratings yet (0 reviews)",
				`<div class="mono">FU</div>`,
				`<link rel="canonical" href="https://api.example.com/artisan/` + validSlug + `" />`,
				`href="https://app.example.com/artisans/` + validSlug + `"`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultShareTestDependencies()
			if test.configure != nil {
				test.configure(&deps)
			}

			recorder, err := runShareRequest(test.slug, deps)
			if err != nil {
				t.Fatalf("handleArtisanSharePage() error = %v", err)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get(echo.HeaderLocation); got != test.wantLocation {
				t.Errorf("Location = %q, want %q", got, test.wantLocation)
			}
			for _, fragment := range test.wantBody {
				if !strings.Contains(recorder.Body.String(), fragment) {
					t.Errorf("body does not contain %q\nbody: %s", fragment, recorder.Body.String())
				}
			}
			for _, fragment := range test.wantBodyAbsent {
				if strings.Contains(recorder.Body.String(), fragment) {
					t.Errorf("body contains internal detail %q\nbody: %s", fragment, recorder.Body.String())
				}
			}
			if test.wantStatus == http.StatusOK {
				if got := recorder.Header().Get("Cache-Control"); got != "public, s-maxage=300, max-age=60" {
					t.Errorf("Cache-Control = %q", got)
				}
			} else if got := recorder.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on error response, want empty", got)
			}
		})
	}
}

func TestHandleArtisanSharePagePopulatedSEOOutput(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.example.com/")
	t.Setenv("APP_PUBLIC_URL", "https://app.example.com/")

	const slug = "alice-kamau-plumber-nairobi"
	deps := defaultShareTestDependencies()
	deps.getUserBySlug = func(_ context.Context, gotSlug string) (*users.User, error) {
		if gotSlug != slug {
			t.Fatalf("slug = %q, want %q", gotSlug, slug)
		}
		return &users.User{
			ID:          "artisan-1",
			FirstName:   "Alice & Sons",
			LastName:    "Kamau <Expert>",
			IsAvailable: true,
			IsVerified:  true,
		}, nil
	}
	deps.getProfileByUserID = func(_ context.Context, userID string) (*Profile, error) {
		if userID != "artisan-1" {
			t.Fatalf("profile user ID = %q, want artisan-1", userID)
		}
		return &Profile{
			Skills:            []string{"Plumbing & Heating"},
			City:              "Nairobi <CBD>",
			YearsOfExperience: 7,
		}, nil
	}
	deps.listReviews = func(_ context.Context, filter bson.M) ([]reviews.Review, error) {
		if got := filter["artisan_id"]; got != "artisan-1" {
			t.Fatalf("review filter artisan_id = %q, want artisan-1", got)
		}
		return []reviews.Review{{Rating: 5}, {Rating: 4}}, nil
	}

	recorder, err := runShareRequest(slug, deps)
	if err != nil {
		t.Fatalf("handleArtisanSharePage() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	wantFragments := []string{
		"Alice &amp; Sons Kamau &lt;Expert&gt; - Plumbing &amp; Heating in Nairobi &lt;CBD&gt; | Fundis",
		`content="Alice &amp; Sons Kamau &lt;Expert&gt; is a Plumbing &amp; Heating in Nairobi &lt;CBD&gt; with 7 years of experience. Hire on Fundis."`,
		"Experience:</span> 7 years",
		"Rating:</span> 4.5 (2 reviews)",
		`<span class="badge badge-green">Available</span>`,
		`<span class="badge badge-blue">Verified</span>`,
		`"ratingValue":"4.5"`,
		`"reviewCount":2`,
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(body, fragment) {
			t.Errorf("body does not contain %q\nbody: %s", fragment, body)
		}
	}
	if strings.Contains(body, "Nairobi <CBD>") || strings.Contains(body, "Kamau <Expert>") {
		t.Errorf("HTML output contains unescaped profile content: %s", body)
	}
}

func defaultShareTestDependencies() artisanShareDependencies {
	return artisanShareDependencies{
		getUserBySlug: func(context.Context, string) (*users.User, error) {
			return &users.User{ID: "artisan-1", FirstName: "Alice"}, nil
		},
		getPublicUserByFundisID: func(context.Context, string) (*users.User, error) {
			return nil, mongo.ErrNoDocuments
		},
		getProfileByUserID: func(context.Context, string) (*Profile, error) {
			return &Profile{}, nil
		},
		listReviews: func(context.Context, bson.M) ([]reviews.Review, error) {
			return nil, nil
		},
	}
}

func runShareRequest(slug string, deps artisanShareDependencies) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/artisan/test", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("slug")
	ctx.SetParamValues(slug)
	return recorder, handleArtisanSharePage(ctx, deps)
}

func TestHandleArtisanSharePageProductionWrapperValidRequest(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("uses production dependencies", func(mt *mtest.T) {
		useMockMongo(mt)
		namespace := func(collection string) string { return mt.DB.Name() + "." + collection }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, namespace("users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "artisan-123"},
				{Key: "slug", Value: "alice-kamau-painter-nakuru"},
				{Key: "first_name", Value: "Alice"},
				{Key: "last_name", Value: "Kamau"},
				{Key: "is_profile_public", Value: true},
			}),
			mtest.CreateCursorResponse(0, namespace("profiles"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "profile-123"},
				{Key: "user_id", Value: "artisan-123"},
				{Key: "city", Value: "Nakuru"},
				{Key: "skills", Value: bson.A{"Painting"}},
			}),
			mtest.CreateCursorResponse(0, namespace("reviews"), mtest.FirstBatch),
		)

		app := echo.New()
		request := httptest.NewRequest(http.MethodGet, "/artisan/alice-kamau-painter-nakuru", nil)
		recorder := httptest.NewRecorder()
		ctx := app.NewContext(request, recorder)
		ctx.SetParamNames("slug")
		ctx.SetParamValues("alice-kamau-painter-nakuru")

		if err := HandleArtisanSharePage(ctx); err != nil {
			mt.Fatalf("HandleArtisanSharePage() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "Alice Kamau - Painting in Nakuru") {
			mt.Errorf("production-wired share page did not contain artisan data: %s", recorder.Body.String())
		}
	})
}

func TestHandleArtisanSharePagePropagatesRequestContextToReviews(t *testing.T) {
	type shareContextKey struct{}
	requestCtx := context.WithValue(context.Background(), shareContextKey{}, "request-value")
	deps := defaultShareTestDependencies()
	deps.listReviews = func(ctx context.Context, _ bson.M) ([]reviews.Review, error) {
		if got := ctx.Value(shareContextKey{}); got != "request-value" {
			t.Errorf("review context value = %#v, want request-value", got)
		}
		return nil, nil
	}

	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/artisan/alice-kamau-plumber-nairobi", nil).WithContext(requestCtx)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("slug")
	ctx.SetParamValues("alice-kamau-plumber-nairobi")
	if err := handleArtisanSharePage(ctx, deps); err != nil {
		t.Fatalf("handleArtisanSharePage() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}
