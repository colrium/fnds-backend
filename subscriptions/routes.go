package subscriptions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"backend/contextutil"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/mongo"
)

type userLookup func(context.Context, string) (*users.User, error)
type accountSummaryLookup func(
	context.Context,
	string,
	time.Time,
) (*AccountSummary, error)

func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/subscriptions/plans", HandleGetPlans)
	g.POST("/subscriptions", HandleCreateSubscription)
	g.GET("/subscriptions", HandleGetUserSubscription)
}

func HandleGetPlans(ctx echo.Context) error {
	return handleGetPlans(users.GetUserByID)(ctx)
}

func handleGetPlans(getUserByID userLookup) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		userID := contextutil.GetUserID(ctx)
		if userID == "" {
			return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
		}
		user, err := getUserByID(ctx.Request().Context(), userID)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
		}
		if err != nil {
			return responses.SendError(ctx, http.StatusInternalServerError, "Failed to fetch subscription plans")
		}
		if user == nil {
			return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
		}
		return responses.SendOK(ctx, PlansForUserType(user.UserType), "Plans retrieved successfully")
	}
}

func HandleCreateSubscription(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	user, err := users.GetUserByID(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	var request PurchaseRequest
	if err := ctx.Bind(&request); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid request body")
	}
	checkout, err := StartCheckout(ctx.Request().Context(), user, request)
	if err != nil {
		switch {
		case errors.Is(err, ErrPlanNotFound),
			errors.Is(err, ErrFreePlan),
			errors.Is(err, ErrResourceRequired):
			return responses.SendError(ctx, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrPlanRoleMismatch):
			return responses.SendError(ctx, http.StatusForbidden, err.Error())
		case errors.Is(err, ErrResourceUnavailable):
			return responses.SendError(ctx, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrPaymentUnavailable):
			return responses.SendError(ctx, http.StatusServiceUnavailable, err.Error())
		default:
			return responses.SendError(ctx, http.StatusBadGateway, "Failed to create payment checkout")
		}
	}
	return responses.SendCreated(ctx, checkout, "Subscription checkout created successfully")
}

func HandleGetUserSubscription(ctx echo.Context) error {
	return handleGetUserSubscription(GetAccountSummary)(ctx)
}

func handleGetUserSubscription(getSummary accountSummaryLookup) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		userID := contextutil.GetUserID(ctx)
		if userID == "" {
			return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
		}
		summary, err := getSummary(
			ctx.Request().Context(),
			userID,
			time.Now().UTC(),
		)
		if err != nil {
			return responses.SendError(ctx, http.StatusInternalServerError, "Failed to fetch subscriptions")
		}
		return responses.SendOK(ctx, summary, "Subscriptions retrieved successfully")
	}
}
