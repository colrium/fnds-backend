package banks

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group) {
	g.GET("/banks", HandleGetBanks)
}

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/bank-accounts/validate", HandleValidateBankAccount)
	g.POST("/bank-accounts", HandleSaveBankAccount)
	g.GET("/bank-accounts", HandleListBankAccounts)
}
