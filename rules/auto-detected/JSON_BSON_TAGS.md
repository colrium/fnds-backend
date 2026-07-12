# JSON/BSON Tag Style

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Use snake_case for both JSON and BSON tags to maintain consistency with MongoDB field naming and API responses. This matches the existing codebase convention.

## CORRECT

```go
type User struct {
    ID            string    `json:"id" bson:"_id"`
    FirstName     string    `json:"first_name" bson:"first_name"`
    LastName      string    `json:"last_name" bson:"last_name"`
    PhoneVerified bool      `json:"phone_verified" bson:"phone_verified"`
    CreatedAt     time.Time `json:"created_at" bson:"created_at"`
}
```

## WRONG

```go
type User struct {
    ID            string    `json:"id" bson:"_id"`
    FirstName     string    `json:"firstName" bson:"firstName"`     // camelCase
    LastName      string    `json:"lastName" bson:"lastName"`       // camelCase
    PhoneVerified bool      `json:"phoneVerified" bson:"phoneVerified"` // camelCase
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| JSON Tags | snake_case |
| BSON Tags | snake_case (matches field name) |
| Exception | `_id` for primary key |
| Optional Fields | Add `omitempty` for fields that may be empty |
