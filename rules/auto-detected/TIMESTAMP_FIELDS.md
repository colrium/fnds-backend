# Timestamp Fields

> Auto-detected by /spartan:scan-rules — review and edit as needed.

All entities have `CreatedAt` and `UpdatedAt` fields. Set `CreatedAt` on insert and `UpdatedAt` on every update. This enables audit trails and temporal queries.

## CORRECT

```go
type Assessment struct {
    ID        string    `json:"id" bson:"_id"`
    Title     string    `json:"title" bson:"title"`
    CreatedAt time.Time `json:"created_at" bson:"created_at"`
    UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateAssessment(a *Assessment) error {
    a.ID = uuid.New().String()
    a.CreatedAt = time.Now()
    a.UpdatedAt = time.Now()
    _, err := configs.MI.DB.Collection("assessments").InsertOne(context.TODO(), a)
    return err
}

func UpdateAssessment(id string, updates bson.M) error {
    updates["updated_at"] = time.Now()
    _, err := configs.MI.DB.Collection("assessments").UpdateOne(
        context.TODO(),
        bson.M{"_id": id},
        bson.M{"$set": updates},
    )
    return err
}
```

## WRONG

```go
type Assessment struct {
    ID    string `json:"id" bson:"_id"`
    Title string `json:"title" bson:"title"`
    // Missing CreatedAt and UpdatedAt
}

func UpdateAssessment(id string, updates bson.M) error {
    // Forgot to set updated_at
    _, err := configs.MI.DB.Collection("assessments").UpdateOne(
        context.TODO(),
        bson.M{"_id": id},
        bson.M{"$set": updates},
    )
    return err
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| Field Type | `time.Time` |
| JSON Tag | `json:"created_at"`, `json:"updated_at"` |
| BSON Tag | `bson:"created_at"`, `bson:"updated_at"` |
| On Create | Set both to `time.Now()` |
| On Update | Always set `updated_at` to `time.Now()` |
