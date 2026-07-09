package banks

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleGetBanks(ctx echo.Context) error {
	banks, err := GetAllBanks()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch banks")
	}
	return responses.SendOK(ctx, banks, "Banks retrieved successfully")
}

func HandleValidateBankAccount(ctx echo.Context) error {
	var req struct {
		AccountNumber string `json:"account_number"`
		BankCode      string `json:"bank_code"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if req.AccountNumber == "" {
		return responses.SendError(ctx, 400, "account_number is required")
	}
	if req.BankCode == "" {
		return responses.SendError(ctx, 400, "bank_code is required")
	}

	bank, err := GetBankByCode(req.BankCode)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid bank code")
	}

	return responses.SendOK(ctx, map[string]interface{}{
		"bank_name": bank.Name,
		"valid":     true,
	}, "Account validated successfully")
}

func HandleSaveBankAccount(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		AccountNumber string `json:"account_number"`
		BankCode      string `json:"bank_code"`
		BankName      string `json:"bank_name"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if req.AccountNumber == "" {
		return responses.SendError(ctx, 400, "account_number is required")
	}
	if req.BankCode == "" {
		return responses.SendError(ctx, 400, "bank_code is required")
	}
	if req.BankName == "" {
		return responses.SendError(ctx, 400, "bank_name is required")
	}

	bank, err := GetBankByCode(req.BankCode)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid bank code")
	}

	ba := BankAccount{
		UserID: userID,
		BankID: bank.ID,
		Name:   req.BankName,
		Number: req.AccountNumber,
	}
	if err := UpsertBankAccount(&ba); err != nil {
		return responses.SendError(ctx, 500, "Failed to save bank account")
	}

	return responses.SendCreated(ctx, ba, "Bank account saved successfully")
}

func HandleListBankAccounts(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	accounts, err := ListBankAccountsByUser(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch bank accounts")
	}
	return responses.SendOK(ctx, accounts, "Bank accounts retrieved successfully")
}
