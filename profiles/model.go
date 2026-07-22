package profiles

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Profile struct {
	ID                             string    `json:"id" bson:"_id"`
	UserID                         string    `json:"user_id" bson:"user_id"`
	IDNumber                       string    `json:"id_number" bson:"id_number"`
	DateOfBirth                    string    `json:"date_of_birth" bson:"date_of_birth"`
	Gender                         string    `json:"gender" bson:"gender"`
	HasSocialMediaHandles          bool      `json:"has_social_media_handles" bson:"has_social_media_handles"`
	SocialMedias                   string    `json:"social_medias" bson:"social_medias"`
	HasSmartphone                  bool      `json:"has_smartphone" bson:"has_smartphone"`
	IsBusinessRegistered           bool      `json:"is_business_registered" bson:"is_business_registered"`
	BusinessName                   string    `json:"business_name" bson:"business_name"`
	BusinessRegistrationNumber     string    `json:"business_registration_number" bson:"business_registration_number"`
	RecentGoodConduct              bool      `json:"recent_good_conduct" bson:"recent_good_conduct"`
	Address                        string    `json:"address" bson:"address"`
	City                           string    `json:"city" bson:"city"`
	State                          string    `json:"state" bson:"state"`
	Zip                            string    `json:"zip" bson:"zip"`
	Country                        string    `json:"country" bson:"country"`
	HighestEducationLevel          string    `json:"highest_education_level" bson:"highest_education_level"`
	Certificate                    string    `json:"certificate" bson:"certificate"`
	Institution                    string    `json:"institution" bson:"institution"`
	OtherQualifications            string    `json:"other_qualifications" bson:"other_qualifications"`
	ProfessionalMembership         bool      `json:"professional_membership" bson:"professional_membership"`
	ProfessionalBody               string    `json:"professional_body" bson:"professional_body"`
	SkillAcquisitionMode           string    `json:"skill_acquisition_mode" bson:"skill_acquisition_mode"`
	YearsOfExperience              int       `json:"years_of_experience" bson:"years_of_experience"`
	NextOfKinName                  string    `json:"next_of_kin_name" bson:"next_of_kin_name"`
	NextOfKinPhoneNumber           string    `json:"next_of_kin_phone_number" bson:"next_of_kin_phone_number"`
	NextOfKinRelationship          string    `json:"next_of_kin_relationship" bson:"next_of_kin_relationship"`
	IsCommunityOrgMember           bool      `json:"is_community_org_member" bson:"is_community_org_member"`
	YearsAtCommunityOrg            int       `json:"years_at_community_org" bson:"years_at_community_org"`
	CharacterReferenceName         string    `json:"character_reference_name" bson:"character_reference_name"`
	CharacterReferenceContact      string    `json:"character_reference_contact" bson:"character_reference_contact"`
	CharacterReferenceRelationship string    `json:"character_reference_relationship" bson:"character_reference_relationship"`
	EarningGoalAmount              float64   `json:"earning_goal_amount" bson:"earning_goal_amount"`
	Skills                         []string  `json:"skills" bson:"skills"`
	Locations                      []string  `json:"locations" bson:"locations"`
	CreatedAt                      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt                      time.Time `json:"updated_at" bson:"updated_at"`
}

func GetProfileByUserID(userID string) (*Profile, error) {
	var profile Profile
	err := configs.MI.DB.Collection("profiles").FindOne(context.TODO(), bson.M{"user_id": userID}).Decode(&profile)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func ListUserIDsBySkill(
	ctx context.Context,
	skillID string,
	skillName string,
) ([]string, error) {
	cursor, err := configs.MI.DB.Collection("profiles").Find(
		ctx,
		skillMatchFilter(skillID, skillName),
		options.Find().SetProjection(bson.M{"user_id": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("finding profiles by skill: %w", err)
	}
	defer cursor.Close(ctx)

	var matches []struct {
		UserID string `bson:"user_id"`
	}
	if err := cursor.All(ctx, &matches); err != nil {
		return nil, fmt.Errorf("decoding profiles by skill: %w", err)
	}
	userIDs := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if match.UserID == "" {
			continue
		}
		if _, exists := seen[match.UserID]; exists {
			continue
		}
		seen[match.UserID] = struct{}{}
		userIDs = append(userIDs, match.UserID)
	}
	return userIDs, nil
}

func skillMatchFilter(skillID, skillName string) bson.M {
	matches := bson.A{}
	if skillID != "" {
		matches = append(matches, bson.M{"skills": skillID})
	}
	if skillName != "" {
		matches = append(matches, bson.M{
			"skills": bson.M{
				"$regex":   "^" + regexp.QuoteMeta(skillName) + "$",
				"$options": "i",
			},
		})
	}
	return bson.M{"$or": matches}
}

func CreateOrUpdateProfile(userID string, updates bson.M) error {
	return upsertProfile(
		context.TODO(),
		configs.MI.DB.Collection("profiles"),
		userID,
		updates,
	)
}

type profileCollection interface {
	UpdateOne(
		context.Context,
		interface{},
		interface{},
		...*options.UpdateOptions,
	) (*mongo.UpdateResult, error)
}

func upsertProfile(
	ctx context.Context,
	collection profileCollection,
	userID string,
	updates bson.M,
) error {
	now := time.Now()
	updates["updated_at"] = now

	_, err := collection.UpdateOne(
		ctx,
		bson.M{"user_id": userID},
		bson.M{
			"$set": updates,
			"$setOnInsert": bson.M{
				"_id":        uuid.New().String(),
				"user_id":    userID,
				"created_at": now,
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("upserting profile: %w", err)
	}
	return nil
}
