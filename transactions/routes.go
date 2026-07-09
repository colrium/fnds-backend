package transactions

import (
	"backend/middleware"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterRoutes(e *echo.Group) {
	auth := e.Group("")
	auth.Use(middleware.JWTAuth)

	auth.GET("/transactions", HandleListTransactions)
	auth.GET("/transactions/:id", HandleGetTransaction)
	auth.POST("/wallet/transactions", HandleCreateWalletTransaction)
}

func HandleListTransactions(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	txns, err := ListTransactions(bson.M{"user_id": userID})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch transactions")
	}
	return responses.SendOK(ctx, txns, "Transactions retrieved successfully")
}

func HandleGetTransaction(ctx echo.Context) error {
	id := ctx.Param("id")
	txn, err := GetTransactionByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Transaction not found")
	}
	return responses.SendOK(ctx, txn, "Transaction retrieved successfully")
}

func HandleCreateWalletTransaction(ctx echo.Context) error {
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var wt WalletTransaction
	if err := ctx.Bind(&wt); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	wt.UserID = userID

	if err := CreateWalletTransaction(&wt); err != nil {
		return responses.SendError(ctx, 500, "Failed to create wallet transaction")
	}
	return responses.SendCreated(ctx, wt, "Wallet transaction created successfully")
}
