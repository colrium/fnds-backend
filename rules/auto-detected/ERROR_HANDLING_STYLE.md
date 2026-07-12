# Error Handling Style

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Return `fmt.Errorf("descriptive message")` for validation errors with user-friendly text. For database/external errors, either wrap with context or pass through. Handlers convert errors to appropriate HTTP responses.

## CORRECT

```go
// service.go — validation errors
func ValidateExternalQuotation(customerEmail, customerName string, items []Item) error {
    if strings.TrimSpace(customerEmail) == "" {
        return fmt.Errorf("customer email is required")
    }
    if !emailRegex.MatchString(customerEmail) {
        return fmt.Errorf("invalid customer email format")
    }
    if len(items) == 0 {
        return fmt.Errorf("at least one item is required")
    }
    return nil
}

// service.go — wrapping DB errors
func GetProfile(userID string) (*User, error) {
    user, err := GetUserByID(userID)
    if err != nil {
        return nil, fmt.Errorf("user not found")
    }
    return user, nil
}

// routes.go — handler error handling
func HandleCreateQuotation(ctx echo.Context) error {
    if err := ValidateCreateQuotation(q.JobRequestID, q.TotalAmount); err != nil {
        return responses.SendError(ctx, 400, err.Error())
    }
    if err := CreateQuotation(&q); err != nil {
        return responses.SendError(ctx, 500, "Failed to create quotation")
    }
    return responses.SendCreated(ctx, q, "Quotation created successfully")
}
```

## WRONG

```go
// service.go
func ValidateExternalQuotation(email string) error {
    if email == "" {
        return errors.New("EMAIL_REQUIRED") // Not user-friendly
    }
    return nil
}

// routes.go
func HandleCreateQuotation(ctx echo.Context) error {
    if err := CreateQuotation(&q); err != nil {
        return responses.SendError(ctx, 500, err.Error()) // Leaks internal error
    }
}
```

## Quick Reference

| Error Type | Convention |
|------------|-----------|
| Validation | `fmt.Errorf("field is required")` — lowercase, user-friendly |
| Not Found | `fmt.Errorf("entity not found")` |
| DB Error (handler) | Generic message: `"Failed to create X"` |
| Wrap Context | `fmt.Errorf("action failed: %w", err)` if needed |
