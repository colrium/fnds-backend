# Validation Functions in Service Layer

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Business validation logic lives in `service.go` files, not in handlers. Validation functions return `error` with clear, user-friendly messages. Handlers call these functions before processing.

## CORRECT

```go
// quotations/service.go
func ValidateCreateQuotation(jobRequestID string, totalAmount float64) error {
    if strings.TrimSpace(jobRequestID) == "" {
        return fmt.Errorf("job request ID is required")
    }
    if totalAmount <= 0 {
        return fmt.Errorf("total amount must be greater than zero")
    }
    return nil
}

// quotations/routes.go
func HandleCreateQuotation(ctx echo.Context) error {
    var q Quotation
    if err := ctx.Bind(&q); err != nil {
        return responses.SendError(ctx, 400, "Invalid request body")
    }

    if err := ValidateCreateQuotation(q.JobRequestID, q.TotalAmount); err != nil {
        return responses.SendError(ctx, 400, err.Error())
    }

    // ... proceed with creation
}
```

## WRONG

```go
// quotations/routes.go — validation in handler
func HandleCreateQuotation(ctx echo.Context) error {
    var q Quotation
    if err := ctx.Bind(&q); err != nil {
        return responses.SendError(ctx, 400, "Invalid request body")
    }

    // Validation logic in handler — don't do this
    if q.JobRequestID == "" {
        return responses.SendError(ctx, 400, "job request ID is required")
    }
    if q.TotalAmount <= 0 {
        return responses.SendError(ctx, 400, "total amount must be greater than zero")
    }

    // ... creation logic
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| Location | `service.go` in each package |
| Function Name | `Validate{Action}{Entity}` |
| Return Type | `error` (nil if valid) |
| Error Messages | User-friendly, lowercase |
| Handler Usage | Call validation, return `SendError(400, err.Error())` |
