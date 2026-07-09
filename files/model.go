package files

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type File struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	FileName  string    `json:"file_name" bson:"file_name"`
	FilePath  string    `json:"file_path" bson:"file_path"`
	MimeType  string    `json:"mime_type" bson:"mime_type"`
	Size      int64     `json:"size" bson:"size"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateFile(file *File) error {
	file.ID = uuid.New().String()
	file.CreatedAt = time.Now()
	file.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("files").InsertOne(context.TODO(), file)
	return err
}

func GetFileByID(id string) (*File, error) {
	var file File
	err := configs.MI.DB.Collection("files").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&file)
	if err != nil {
		return nil, err
	}
	return &file, nil
}

func GetFilesByUserID(userID string) ([]File, error) {
	cursor, err := configs.MI.DB.Collection("files").Find(context.TODO(), bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var fileList []File
	if err := cursor.All(context.TODO(), &fileList); err != nil {
		return nil, err
	}
	return fileList, nil
}

func DeleteFile(id string) error {
	_, err := configs.MI.DB.Collection("files").DeleteOne(context.TODO(), bson.M{"_id": id})
	return err
}
