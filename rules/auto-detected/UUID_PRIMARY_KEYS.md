# UUID Primary Keys

> Auto-detected by /spartan:scan-rules — review and edit as needed.

All MongoDB documents use UUID strings as primary keys (`_id` field), not ObjectId. This ensures IDs are URL-safe, predictable, and can be generated client-side if needed. Use `github.com/google/uuid` to generate IDs.

## CORRECT

```go
import "github.com/google/uuid"

type User struct {
    ID        string    `json:"id" bson:"_id"`
    // ...
}

func CreateNewUser(user *User) error {
    user.ID = uuid.New().String()
    user.CreatedAt = time.Now()
    _, err := configs.MI.DB.Collection("users").InsertOne(context.TODO(), user)
    return err
}
```

## WRONG

```go
import "go.mongodb.org/mongo-driver/bson/primitive"

type User struct {
    ID primitive.ObjectID `json:"id" bson:"_id"` // Don't use ObjectId
    // ...
}

func CreateNewUser(user *User) error {
    user.ID = primitive.NewObjectID() // Don't do this
    // ...
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| ID Type | `string` |
| Generation | `uuid.New().String()` |
| Import | `github.com/google/uuid` |
| BSON Tag | `bson:"_id"` |
| JSON Tag | `json:"id"` |
