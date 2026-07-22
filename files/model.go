package files

import (
	"context"
	"fmt"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type File struct {
	ID             string    `json:"id" bson:"_id"`
	UserID         string    `json:"user_id" bson:"user_id"`
	DocumentTypeID string    `json:"document_type_id,omitempty" bson:"document_type_id,omitempty"`
	FileName       string    `json:"file_name" bson:"file_name"`
	FileURL        string    `json:"file_url" bson:"file_url"`
	RelativePath   string    `json:"relative_path,omitempty" bson:"relative_path,omitempty"`
	MimeType       string    `json:"mime_type" bson:"mime_type"`
	Size           int64     `json:"size" bson:"size"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

type Repository interface {
	Create(ctx context.Context, file *File) error
	ListByUserID(ctx context.Context, userID string) ([]File, error)
	GetByIDForUser(ctx context.Context, id, userID string) (*File, error)
	DeleteByIDForUser(ctx context.Context, id, userID string) error
}

type MongoRepository struct{}

func NewMongoRepository() *MongoRepository {
	return &MongoRepository{}
}

func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("files").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "file_url", Value: 1},
		},
		Options: options.Index().
			SetName("files_user_url_unique").
			SetUnique(true).
			SetPartialFilterExpression(bson.M{
				"file_url": bson.M{"$type": "string"},
			}),
	})
	if err != nil {
		return fmt.Errorf("creating file indexes: %w", err)
	}
	return nil
}

func (r *MongoRepository) Create(ctx context.Context, file *File) error {
	now := time.Now().UTC()
	file.ID = uuid.New().String()
	file.CreatedAt = now
	file.UpdatedAt = now

	filter := bson.M{"user_id": file.UserID, "file_url": file.FileURL}
	update := bson.M{"$setOnInsert": file}
	result := configs.MI.DB.Collection("files").FindOneAndUpdate(
		ctx,
		filter,
		update,
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	)
	if err := result.Decode(file); err != nil {
		return fmt.Errorf("creating file record: %w", err)
	}
	return nil
}

func (r *MongoRepository) ListByUserID(ctx context.Context, userID string) ([]File, error) {
	cursor, err := configs.MI.DB.Collection("files").Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("listing file records: %w", err)
	}
	defer cursor.Close(ctx)

	fileList := make([]File, 0)
	if err := cursor.All(ctx, &fileList); err != nil {
		return nil, fmt.Errorf("decoding file records: %w", err)
	}
	return fileList, nil
}

func (r *MongoRepository) GetByIDForUser(ctx context.Context, id, userID string) (*File, error) {
	var file File
	err := configs.MI.DB.Collection("files").
		FindOne(ctx, bson.M{"_id": id, "user_id": userID}).
		Decode(&file)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting file record: %w", err)
	}
	return &file, nil
}

func (r *MongoRepository) DeleteByIDForUser(ctx context.Context, id, userID string) error {
	result, err := configs.MI.DB.Collection("files").
		DeleteOne(ctx, bson.M{"_id": id, "user_id": userID})
	if err != nil {
		return fmt.Errorf("deleting file record: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}
