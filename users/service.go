package users

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"backend/configs"
	"backend/counties"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/crypto/bcrypt"
)

func SendOTP(ctx context.Context, userID string) error {
	user, err := GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.PhoneVerified {
		return fmt.Errorf("phone already verified")
	}
	otp := GenerateOTP()
	return UpdateUser(userID, bson.M{"otp": otp})
}

func VerifyOTP(ctx context.Context, userID, otp string) error {
	user, err := GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.PhoneVerified {
		return fmt.Errorf("phone already verified")
	}
	if user.Otp != otp {
		return fmt.Errorf("invalid OTP")
	}
	return UpdateUser(userID, bson.M{
		"otp":            "",
		"phone_verified": true,
		"status":         StatusActive,
		"is_active":      true,
	})
}

func ResendOTP(ctx context.Context, userID string) error {
	return SendOTP(ctx, userID)
}

func ForgotPassword(phone, userType string) error {
	if phone == "" || userType == "" {
		return fmt.Errorf("phone and user_type are required")
	}
	_, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return fmt.Errorf("phone number does not exist")
	}
	otp := GenerateOTP()
	user, _ := GetUserByPhoneAndType(phone, userType)
	return UpdateUser(user.ID, bson.M{"otp": otp})
}

func ResetPassword(phone, otp, password, confirmPassword, userType string) error {
	if phone == "" || otp == "" || password == "" || confirmPassword == "" || userType == "" {
		return fmt.Errorf("all fields are required")
	}
	if password != confirmPassword {
		return fmt.Errorf("passwords do not match")
	}
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	user, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return fmt.Errorf("invalid phone number")
	}
	if user.Otp != otp {
		return fmt.Errorf("invalid OTP")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	return UpdateUser(user.ID, bson.M{"password": hashed, "otp": "", "phone_verified": true})
}

func UpdateFCMToken(userID, token string) error {
	if token == "" {
		return fmt.Errorf("token is required")
	}
	return UpdateUser(userID, bson.M{"fcm_token": token})
}

func GetProfile(ctx context.Context, userID string) (*User, error) {
	user, err := GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return user, nil
}

func UpdateProfile(ctx context.Context, userID string, req ProfileUpdateRequest) error {
	if req.County != "" {
		canonical, ok := counties.Normalize(req.County)
		if !ok {
			return counties.ErrInvalidCounty
		}
		req.County = canonical
	}

	user, err := GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	userUpdates := bson.M{}
	if req.FirstName != "" {
		userUpdates["first_name"] = req.FirstName
	}
	if req.LastName != "" {
		userUpdates["last_name"] = req.LastName
	}
	if len(userUpdates) > 0 {
		_ = UpdateUser(userID, userUpdates)
	}

	profileUpdates := bson.M{}
	if req.IDNumber != "" {
		profileUpdates["id_number"] = req.IDNumber
	}
	if req.DateOfBirth != "" {
		profileUpdates["date_of_birth"] = req.DateOfBirth
	}
	if req.Gender != "" {
		profileUpdates["gender"] = req.Gender
	}
	if req.HasSocialMediaHandles != nil {
		profileUpdates["has_social_media_handles"] = *req.HasSocialMediaHandles
	}
	if req.SocialMedias != "" {
		profileUpdates["social_medias"] = req.SocialMedias
	}
	if req.HasSmartphone != nil {
		profileUpdates["has_smartphone"] = *req.HasSmartphone
	}
	if req.IsBusinessRegistered != nil {
		profileUpdates["is_business_registered"] = *req.IsBusinessRegistered
	}
	if req.BusinessName != "" {
		profileUpdates["business_name"] = req.BusinessName
	}
	if req.BusinessRegistrationNumber != "" {
		profileUpdates["business_registration_number"] = req.BusinessRegistrationNumber
	}
	if req.RecentGoodConduct != nil {
		profileUpdates["recent_good_conduct"] = *req.RecentGoodConduct
	}
	if req.Address != "" {
		profileUpdates["address"] = req.Address
	}
	if req.City != "" {
		profileUpdates["city"] = req.City
	}
	if req.County != "" {
		profileUpdates["county"] = req.County
	}
	if req.Zip != "" {
		profileUpdates["zip"] = req.Zip
	}
	if req.Country != "" {
		profileUpdates["country"] = req.Country
	}
	if len(profileUpdates) > 0 {
		profileUpdates["updated_at"] = time.Now()
		_, _ = configs.MI.DB.Collection("profiles").UpdateOne(
			context.TODO(),
			bson.M{"user_id": user.ID},
			bson.M{"$set": profileUpdates},
		)
	}

	if req.SkillAcquisitionMode != "" || req.YearsOfExperience > 0 {
		proTraining := bson.M{"user_id": user.ID, "updated_at": time.Now()}
		if req.SkillAcquisitionMode != "" {
			proTraining["skill_acquisition_mode"] = req.SkillAcquisitionMode
		}
		if req.YearsOfExperience > 0 {
			proTraining["years_of_experience"] = req.YearsOfExperience
		}
		_, _ = configs.MI.DB.Collection("professional_trainings").UpdateOne(
			context.TODO(),
			bson.M{"user_id": user.ID},
			bson.M{"$set": proTraining},
		)
	}

	if len(req.References) > 0 {
		coll := configs.MI.DB.Collection("competency_references")
		for _, ref := range req.References {
			refData := bson.M{"user_id": user.ID, "updated_at": time.Now()}
			if ref.Name != "" {
				refData["name"] = ref.Name
			}
			if ref.PhoneNumber != "" {
				refData["phone_number"] = ref.PhoneNumber
			}
			if ref.JobTypeID != "" {
				refData["job_type_id"] = ref.JobTypeID
			}
			if ref.JobTitle != "" {
				refData["job_title"] = ref.JobTitle
			}
			if ref.Email != "" {
				refData["email"] = ref.Email
			}
			if ref.Location != "" {
				refData["location"] = ref.Location
			}

			if ref.ID != "" {
				_, _ = coll.UpdateOne(
					context.TODO(),
					bson.M{"_id": ref.ID, "user_id": user.ID},
					bson.M{"$set": refData},
				)
			} else {
				refData["_id"] = uuid.New().String()
				refData["created_at"] = time.Now()
				_, _ = coll.InsertOne(context.TODO(), refData)
			}
		}
	}

	if req.WorkingHours != nil {
		coll := configs.MI.DB.Collection("working_hours")
		_, _ = coll.DeleteMany(context.TODO(), bson.M{"user_id": user.ID})
		for _, wh := range req.WorkingHours {
			_, _ = coll.InsertOne(context.TODO(), bson.M{
				"_id":          uuid.New().String(),
				"user_id":      user.ID,
				"day":          wh.Day,
				"opening_time": wh.OpeningTime,
				"closing_time": wh.ClosingTime,
				"created_at":   time.Now(),
				"updated_at":   time.Now(),
			})
		}
	}

	if req.PastEmployers != nil {
		coll := configs.MI.DB.Collection("past_employers")
		for _, pe := range req.PastEmployers {
			peData := bson.M{"user_id": user.ID, "updated_at": time.Now()}
			if pe.Name != "" {
				peData["name"] = pe.Name
			}
			if pe.PhoneNumber != "" {
				peData["phone_number"] = pe.PhoneNumber
			}
			if pe.Location != "" {
				peData["location"] = pe.Location
			}

			if pe.ID != "" {
				_, _ = coll.UpdateOne(
					context.TODO(),
					bson.M{"_id": pe.ID, "user_id": user.ID},
					bson.M{"$set": peData},
				)
			} else {
				peData["_id"] = uuid.New().String()
				peData["created_at"] = time.Now()
				_, _ = coll.InsertOne(context.TODO(), peData)
			}
		}
	}

	if req.LanguageProficiencies != nil {
		coll := configs.MI.DB.Collection("language_proficiencies")
		for _, lp := range req.LanguageProficiencies {
			lpData := bson.M{"user_id": user.ID, "updated_at": time.Now()}
			if lp.Language != "" {
				lpData["language"] = lp.Language
			}
			if lp.ProficiencyLevel != "" {
				lpData["proficiency_level"] = lp.ProficiencyLevel
			}
			if lp.WrittenSkills != "" {
				lpData["written_skills"] = lp.WrittenSkills
			}

			if lp.ID != "" {
				_, _ = coll.UpdateOne(
					context.TODO(),
					bson.M{"_id": lp.ID, "user_id": user.ID},
					bson.M{"$set": lpData},
				)
			} else {
				lpData["_id"] = uuid.New().String()
				lpData["created_at"] = time.Now()
				_, _ = coll.InsertOne(context.TODO(), lpData)
			}
		}
	}

	return nil
}

type ProfileUpdateRequest struct {
	FirstName                      string                     `json:"first_name"`
	LastName                       string                     `json:"last_name"`
	IDNumber                       string                     `json:"id_number"`
	Locations                      []string                   `json:"locations"`
	Skills                         []string                   `json:"skills"`
	DateOfBirth                    string                     `json:"date_of_birth"`
	Gender                         string                     `json:"gender"`
	HasSocialMediaHandles          *bool                      `json:"has_social_media_handles"`
	SocialMedias                   string                     `json:"social_medias"`
	HasSmartphone                  *bool                      `json:"has_smartphone"`
	IsBusinessRegistered           *bool                      `json:"is_business_registered"`
	BusinessName                   string                     `json:"business_name"`
	BusinessRegistrationNumber     string                     `json:"business_registration_number"`
	RecentGoodConduct              *bool                      `json:"recent_good_conduct"`
	Address                        string                     `json:"address"`
	City                           string                     `json:"city"`
	County                         string                     `json:"county"`
	Zip                            string                     `json:"zip"`
	Country                        string                     `json:"country"`
	SkillAcquisitionMode           string                     `json:"skill_acquisition_mode"`
	YearsOfExperience              int                        `json:"years_of_experience"`
	NextOfKinName                  string                     `json:"next_of_kin_name"`
	NextOfKinPhoneNumber           string                     `json:"next_of_kin_phone_number"`
	NextOfKinRelationship          string                     `json:"next_of_kin_relationship"`
	IsCommunityOrgMember           *bool                      `json:"is_community_org_member"`
	YearsAtCommunityOrg            int                        `json:"years_at_community_org"`
	CharacterReferenceName         string                     `json:"character_reference_name"`
	CharacterReferenceContact      string                     `json:"character_reference_contact"`
	CharacterReferenceRelationship string                     `json:"character_reference_relationship"`
	EarningGoalAmount              float64                    `json:"earning_goal_amount"`
	WorkingHours                   []WorkingHourInput         `json:"working_hours"`
	PastEmployers                  []PastEmployerInput        `json:"past_employers"`
	LanguageProficiencies          []LanguageProficiencyInput `json:"language_proficiencies"`
	References                     []ReferenceInput           `json:"references"`
}

type WorkingHourInput struct {
	Day         string `json:"day"`
	OpeningTime string `json:"opening_time"`
	ClosingTime string `json:"closing_time"`
}

type PastEmployerInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
	Location    string `json:"location"`
}

type LanguageProficiencyInput struct {
	ID               string `json:"id"`
	Language         string `json:"language"`
	ProficiencyLevel string `json:"proficiency_level"`
	WrittenSkills    string `json:"written_skills"`
}

type ReferenceInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
	JobTypeID   string `json:"job_type_id"`
	JobTitle    string `json:"job_title"`
	Email       string `json:"email"`
	Location    string `json:"location"`
}

// GenerateOTP returns a cryptographically-random 6-digit verification code.
func GenerateOTP() string {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		// crypto/rand should never fail; fall back to a time-derived value.
		n = big.NewInt(time.Now().UnixNano() % 1000000)
	}
	return fmt.Sprintf("%06d", n.Int64())
}
