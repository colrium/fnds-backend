package chat

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Chat struct {
	ID           string    `json:"id" bson:"_id"`
	JobRequestID string    `json:"job_request_id" bson:"job_request_id"`
	SentBy       string    `json:"sent_by" bson:"sent_by"`
	SentTo       string    `json:"sent_to" bson:"sent_to"`
	Message      string    `json:"message" bson:"message"`
	ChatType     string    `json:"chat_type" bson:"chat_type"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
}

func CreateChat(c *Chat) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now()
	_, err := configs.MI.DB.Collection("chats").InsertOne(context.TODO(), c)
	return err
}

func ListChatsByJobRequest(jobRequestID string) ([]Chat, error) {
	cursor, err := configs.MI.DB.Collection("chats").Find(context.TODO(), bson.M{"job_request_id": jobRequestID})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(context.TODO()) }()
	var chats []Chat
	if err := cursor.All(context.TODO(), &chats); err != nil {
		return nil, err
	}
	return chats, nil
}
