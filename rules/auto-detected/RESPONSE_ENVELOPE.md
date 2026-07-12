# Standardized Response Envelope

> Auto-detected by /spartan:scan-rules — review and edit as needed.

All API responses use the `responses` package for consistent JSON structure. Never write raw JSON directly — always use `SendOK`, `SendCreated`, or `SendError`. This ensures predictable response format for clients.

## CORRECT

```go
import "backend/responses"

func HandleGetUser(ctx echo.Context) error {
    user, err := GetUserByID(id)
    if err != nil {
        return responses.SendError(ctx, 404, "User not found")
    }
    return responses.SendOK(ctx, user, "User retrieved successfully")
}

func HandleCreateUser(ctx echo.Context) error {
    user, err := CreateUser(req)
    if err != nil {
        return responses.SendError(ctx, 500, "Failed to create user")
    }
    return responses.SendCreated(ctx, user, "User created successfully")
}
```

## WRONG

```go
func HandleGetUser(ctx echo.Context) error {
    user, err := GetUserByID(id)
    if err != nil {
        return ctx.JSON(404, map[string]string{"error": "not found"}) // Inconsistent
    }
    return ctx.JSON(200, user) // Missing envelope
}

func HandleCreateUser(ctx echo.Context) error {
    return ctx.JSON(201, user) // Should use SendCreated
}
```

## Quick Reference

| Function | HTTP Status | Use Case |
|----------|-------------|----------|
| `SendOK(ctx, data, message)` | 200 | Successful GET, PATCH |
| `SendCreated(ctx, data, message)` | 201 | Successful POST |
| `SendError(ctx, status, message)` | Any | Error responses |

## Response Structure

```json
// Success
{
  "success": true,
  "data": { ... },
  "message": "Operation completed"
}

// Error
{
  "success": false,
  "message": "Error description"
}
```
