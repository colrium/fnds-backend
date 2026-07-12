# Route Registration Pattern

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Each package exports `RegisterAuthRoutes` and/or `RegisterPublicRoutes` functions. `main.go` calls these to mount routes. Public routes have no auth; authenticated routes go through the JWT middleware group.

## CORRECT

```go
// auth/routes.go
func RegisterPublicRoutes(g *echo.Group) {
    g.POST("/register", HandleUserRegistration)
    g.POST("/login", HandleUserLogin)
}

func RegisterAuthRoutes(g *echo.Group) {
    g.POST("/admin/users", HandleCreateUserViaAdmin)
}

// main.go
func main() {
    // ...
    api := app.Group("/api/v1")

    auth.RegisterPublicRoutes(api)
    applications.RegisterRoutes(api)
    // ... other public routes

    authGroup := api.Group("")
    authGroup.Use(middleware.JWTAuth)

    auth.RegisterAuthRoutes(authGroup)
    profiles.RegisterAuthRoutes(authGroup)
    // ... other authenticated routes
}
```

## WRONG

```go
// auth/routes.go
func SetupRoutes(app *echo.Echo) {
    app.POST("/api/v1/register", HandleUserRegistration) // Hardcoded path
    app.POST("/api/v1/login", HandleUserLogin)
}

// main.go
func main() {
    auth.SetupRoutes(app) // No separation of public/auth routes
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| Export Function | `RegisterPublicRoutes(g *echo.Group)` |
| Export Function | `RegisterAuthRoutes(g *echo.Group)` |
| Parameter | `*echo.Group` (not `*echo.Echo`) |
| Public Routes | Mounted directly on `/api/v1` |
| Auth Routes | Mounted on group with `JWTAuth` middleware |
