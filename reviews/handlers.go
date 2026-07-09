package reviews

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleCreateReview(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
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
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	reviewList, err := ListReviews(map[string]interface{}{"customer_id": userID})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch reviews")
	}
	return responses.SendOK(ctx, reviewList, "Reviews retrieved successfully")
}

func HandleListReviewsByArtisan(ctx echo.Context) error {
	artisanID := ctx.Param("artisanId")
	reviewList, err := ListReviews(map[string]interface{}{"artisan_id": artisanID})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch reviews")
	}
	return responses.SendOK(ctx, reviewList, "Reviews retrieved successfully")
}
