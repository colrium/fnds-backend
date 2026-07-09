package reviews

import (
	"backend/middleware"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterRoutes(e *echo.Group) {
	auth := e.Group("")
	auth.Use(middleware.JWTAuth)

	auth.POST("/reviews", HandleCreateReview)
	auth.GET("/reviews", HandleListReviews)
}

func HandleCreateReview(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var review Review
	if err := ctx.Bind(&review); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	review.CustomerID = userID
	if err := CreateReview(&review); err != nil {
		return responses.SendError(ctx, 500, "Failed to create review")
	}
	return responses.SendCreated(ctx, review, "Review created successfully")
}

func HandleListReviews(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	reviews, err := ListReviews(bson.M{"customer_id": userID})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch reviews")
	}
	return responses.SendOK(ctx, reviews, "Reviews retrieved successfully")
}
