package profiles

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Profile struct {
	ID                        string   `json:"id" bson:"_id"`
	UserID                    string   `json:"user_id" bson:"user_id"`
	IDNumber                  string   `json:"id_number" bson:"id_number"`
	DateOfBirth               string   `json:"date_of_birth" bson:"date_of_birth"`
	Gender                    string   `json:"gender" bson:"gender"`
	HasSocialMediaHandles     bool     `json:"has_social_media_handles" bson:"has_social_media_handles"`
	SocialMedias              string   `json:"social_medias" bson:"social_medias"`
	HasSmartphone             bool     `json:"has_smartphone" bson:"has_smartphone"`
	IsBusinessRegistered      bool     `json:"is_business_registered" bson:"is_business_registered"`
	BusinessName              string   `json:"business_name" bson:"business_name"`
	BusinessRegistrationNumber string  `json:"business_registration_number" bson:"business_registration_number"`
	RecentGoodConduct         bool     `json:"recent_good_conduct" bson:"recent_good_conduct"`
	Address                   string   `json:"address" bson:"address"`
	City                      string   `json:"city" bson:"city"`
	State                     string   `json:"state" bson:"state"`
	Zip                       string   `json:"zip" bson:"zip"`
	Country                   string   `json:"country" bson:"country"`
	Skills                    []string `json:"skills" bson:"skills"`
	CreatedAt                 time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at" bson:"updated_at"`
}

func GetProfileByUserID(userID string) (*Profile, error) {
	var profile Profile
	err := configs.MI.DB.Collection("profiles").FindOne(context.TODO(), bson.M{"user_id": userID}).Decode(&profile)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func CreateOrUpdateProfile(userID string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	result := configs.MI.DB.Collection("profiles").FindOneAndUpdate(
		context.TODO(),
		bson.M{"user_id": userID},
		bson.M{"$set": updates},
	)
	if result.Err() != nil {
		updates["user_id"] = userID
		updates["created_at"] = time.Now()
		updates["_id"] = uuid.New().String()
		_, err := configs.MI.DB.Collection("profiles").InsertOne(context.TODO(), updates)
		return err
	}
	return nil
}
