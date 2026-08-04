package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/contextutil"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestHandleGetPlansFiltersByAuthenticatedUserType(t *testing.T) {
	tests := []struct {
		name         string
		userType     string
		wantAudience Audience
		wantCount    int
	}{
		{
			name:         "artisan receives worker plans",
			userType:     "artisan",
			wantAudience: AudienceWorker,
			wantCount:    5,
		},
		{
			name:         "customer receives employer plans",
			userType:     "customer",
			wantAudience: AudienceEmployer,
			wantCount:    4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder := plansTestContext("user-1")
			handler := handleGetPlans(func(context.Context, string) (*users.User, error) {
				return &users.User{ID: "user-1", UserType: tt.userType}, nil
			})

			if err := handler(ctx); err != nil {
				t.Fatalf("handleGetPlans() error = %v", err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}

			var response struct {
				Data []SubscriptionPlan `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(response.Data) != tt.wantCount {
				t.Fatalf("returned %d plans, want %d", len(response.Data), tt.wantCount)
			}
			for _, plan := range response.Data {
				if plan.Audience != tt.wantAudience {
					t.Errorf("plan %q audience = %q, want %q", plan.ID, plan.Audience, tt.wantAudience)
				}
			}
		})
	}
}

func TestHandleGetPlansPropagatesRequestContextToUserLookup(t *testing.T) {
	type contextKey struct{}
	requestContext, cancel := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, "request-value"),
	)
	cancel()

	server := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/subscriptions/plans", nil).
		WithContext(requestContext)
	recorder := httptest.NewRecorder()
	ctx := server.NewContext(request, recorder)
	// contextutil.SetUser derives a new context.Context from the request
	// context (it stamps the authenticated user id for log correlation), so
	// gotContext is no longer required to be requestContext by identity. What
	// still must hold is that every property of requestContext — cancellation,
	// deadline, and values — is preserved by that derivation.
	contextutil.SetUser(ctx, "user-1")

	lookupCalled := false
	handler := handleGetPlans(func(gotContext context.Context, userID string) (*users.User, error) {
		lookupCalled = true
		if !errors.Is(gotContext.Err(), context.Canceled) {
			t.Errorf("user lookup context error = %v, want context.Canceled", gotContext.Err())
		}
		if got := gotContext.Value(contextKey{}); got != "request-value" {
			t.Errorf("user lookup context value = %v, want request-value", got)
		}
		if userID != "user-1" {
			t.Errorf("user lookup user ID = %q, want user-1", userID)
		}
		return &users.User{ID: userID, UserType: "artisan"}, nil
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("handleGetPlans() error = %v", err)
	}
	if !lookupCalled {
		t.Fatal("user lookup was not called")
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestHandleGetPlansClassifiesUserLookupFailures(t *testing.T) {
	tests := []struct {
		name       string
		lookupErr  error
		wantStatus int
	}{
		{
			name:       "missing user is unauthorized",
			lookupErr:  mongo.ErrNoDocuments,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "database failure is internal error",
			lookupErr:  errors.New("database unavailable"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder := plansTestContext("user-1")
			handler := handleGetPlans(func(context.Context, string) (*users.User, error) {
				return nil, tt.lookupErr
			})

			if err := handler(ctx); err != nil {
				t.Fatalf("handleGetPlans() error = %v", err)
			}
			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
		})
	}
}

func TestHandleGetUserSubscriptionReturnsActiveAndHistory(t *testing.T) {
	ctx, recorder := plansTestContext("user-1")
	handler := handleGetUserSubscription(
		func(
			context.Context,
			string,
			time.Time,
		) (*AccountSummary, error) {
			return &AccountSummary{
				Subscriptions: []UserSubscription{{ID: "active-1"}},
				History:       []UserSubscription{{ID: "active-1"}, {ID: "expired-1"}},
			}, nil
		},
	)

	if err := handler(ctx); err != nil {
		t.Fatalf("handleGetUserSubscription() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response struct {
		Data AccountSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.Subscriptions) != 1 {
		t.Errorf("active subscriptions = %d, want 1", len(response.Data.Subscriptions))
	}
	if len(response.Data.History) != 2 {
		t.Errorf("subscription history = %d, want 2", len(response.Data.History))
	}
}

func plansTestContext(userID string) (echo.Context, *httptest.ResponseRecorder) {
	server := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/subscriptions/plans", nil)
	recorder := httptest.NewRecorder()
	ctx := server.NewContext(request, recorder)
	contextutil.SetUser(ctx, userID)
	return ctx, recorder
}
