package users

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

type User struct {
	ID            string    `json:"id" bson:"_id"`
	FirstName     string    `json:"first_name" bson:"first_name"`
	LastName      string    `json:"last_name" bson:"last_name"`
	Email         string    `json:"email" bson:"email"`
	Phone         string    `json:"phone" bson:"phone"`
	Password      []byte    `json:"-" bson:"password"`
	UserType      string    `json:"user_type" bson:"user_type"`
	Status        string    `json:"status" bson:"status"`
	IsActive      bool      `json:"is_active" bson:"is_active"`
	Otp           string    `json:"-" bson:"otp,omitempty"`
	OtpExpiresAt  time.Time `json:"-" bson:"otp_expires_at,omitempty"`
	OtpAttempts   int       `json:"-" bson:"otp_attempts,omitempty"`
	PhoneVerified bool      `json:"phone_verified" bson:"phone_verified"`
	FundisID      string    `json:"fundis_id,omitempty" bson:"fundis_id,omitempty"`
	AvatarID      string    `json:"avatar_id,omitempty" bson:"avatar_id,omitempty"`
	FCMToken      string    `json:"fcm_token,omitempty" bson:"fcm_token,omitempty"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateNewUser(user *User) error {
	user.ID = uuid.New().String()
	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("users").InsertOne(context.TODO(), user)
	return err
}

func GetUserByID(id string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByEmail(email string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"email": email}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByPhone(phone string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"phone": phone}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByEmailAndType(email, userType string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"email": email, "user_type": userType}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByPhoneAndType(phone, userType string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"phone": phone, "user_type": userType}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByFundisID(fundisID string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"fundis_id": fundisID, "user_type": "artisan"}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func UpdateUser(id string, update bson.M) error {
	update["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("users").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": update})
	return err
}

func ListArtisans(page, limit int64) ([]User, int64, error) {
	filter := bson.M{"user_type": "artisan"}
	opts := options.Find().SetSkip((page - 1) * limit).SetLimit(limit)
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.TODO())

	var users []User
	if err := cursor.All(context.TODO(), &users); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("users").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func ListAllUsers(filter bson.M, page, limit int64) ([]User, int64, error) {
	opts := options.Find().SetSkip((page - 1) * limit).SetLimit(limit)
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.TODO())

	var userList []User
	if err := cursor.All(context.TODO(), &userList); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("users").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}

	return userList, total, nil
}
