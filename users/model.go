package users

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
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
	IsAvailable   bool      `json:"is_available" bson:"is_available"`
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

// EnsureIndexes creates the unique constraints for the users collection.
// Uniqueness is scoped per user_type so the same person can hold both a
// customer and an artisan account. Email is enforced only when present, since
// it is optional at registration.
func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("users").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "phone", Value: 1}, {Key: "user_type", Value: 1}},
			Options: options.Index().SetName("uniq_phone_user_type").SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "email", Value: 1}, {Key: "user_type", Value: 1}},
			Options: options.Index().
				SetName("uniq_email_user_type").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"email": bson.M{"$type": "string", "$gt": ""}}),
		},
	})
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

func GetUsersByIDs(ids []string) ([]User, error) {
	users := []User{}
	if len(ids) == 0 {
		return users, nil
	}
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	if err := cursor.All(context.TODO(), &users); err != nil {
		return nil, err
	}
	return users, nil
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

func IncrementOtpAttempts(id string) error {
	_, err := configs.MI.DB.Collection("users").UpdateOne(context.TODO(),
		bson.M{"_id": id},
		bson.M{"$inc": bson.M{"otp_attempts": 1}, "$set": bson.M{"updated_at": time.Now()}})
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
