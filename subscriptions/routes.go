package subscriptions

import (
	"backend/middleware"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	auth := e.Group("")
	auth.Use(middleware.JWTAuth)

	auth.GET("/subscriptions/plans", HandleGetPlans)
	auth.POST("/subscriptions", HandleCreateSubscription)
	auth.GET("/subscriptions", HandleGetUserSubscription)
}

func HandleGetPlans(ctx echo.Context) error {
	plans, err := GetAllPlans()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch plans")
	}
	return responses.SendOK(ctx, plans, "Plans retrieved successfully")
}

func HandleCreateSubscription(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var sub UserSubscription
	if err := ctx.Bind(&sub); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	sub.UserID = userID
	sub.IsActive = true

	if err := CreateUserSubscription(&sub); err != nil {
		return responses.SendError(ctx, 500, "Failed to create subscription")
	}
	return responses.SendCreated(ctx, sub, "Subscription created successfully")
}

func HandleGetUserSubscription(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	sub, err := GetUserSubscription(userID)
	if err != nil {
		return responses.SendError(ctx, 404, "No active subscription found")
	}
	return responses.SendOK(ctx, sub, "Subscription retrieved successfully")
}
