# Context User ID Extraction

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Authenticated handlers retrieve the current user ID from Echo context using `contextutil.GetUserID(ctx)`. The JWT middleware sets this value after validating the token. Always check for empty string before proceeding.

## CORRECT

```go
import "backend/contextutil"

func HandleSendOtp(ctx echo.Context) error {
    userID := contextutil.GetUserID(ctx)
    if userID == "" {
        return responses.SendError(ctx, 401, "Unauthorized")
    }

    user, err := GetUserByID(userID)
    if err != nil {
        return responses.SendError(ctx, 400, "Invalid user")
    }

    // ... proceed with business logic
}
```

## WRONG

```go
func HandleSendOtp(ctx echo.Context) error {
    // Don't access JWT claims directly
    user := ctx.Get("user").(*jwt.Token)
    claims := user.Claims.(jwt.MapClaims)
    userID := claims["user_id"].(string)

    // Or worse, parsing the Authorization header again
    authHeader := ctx.Request().Header.Get("Authorization")
    // ... manual parsing
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| Import | `backend/contextutil` |
| Get User ID | `contextutil.GetUserID(ctx)` |
| Return Type | `string` (empty if not set) |
| Check | `if userID == "" { return 401 }` |
| Set by | `middleware.JWTAuth` |
