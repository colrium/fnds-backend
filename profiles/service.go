package profiles

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"backend/configs"
	"backend/users"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type ProfileUpdateRequest struct {
	Email                          string                     `json:"email"`
	IDNumber                       string                     `json:"id_number"`
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
	State                          string                     `json:"state"`
	Zip                            string                     `json:"zip"`
	Country                        string                     `json:"country"`
	HighestEducationLevel          string                     `json:"highest_education_level"`
	Certificate                    string                     `json:"certificate"`
	Institution                    string                     `json:"institution"`
	OtherQualifications            string                     `json:"other_qualifications"`
	ProfessionalMembership         *bool                      `json:"professional_membership"`
	ProfessionalBody               string                     `json:"professional_body"`
	SkillAcquisitionMode           string                     `json:"skill_acquisition_mode"`
	YearsOfExperience              *int                       `json:"years_of_experience"`
	NextOfKinName                  string                     `json:"next_of_kin_name"`
	NextOfKinPhoneNumber           string                     `json:"next_of_kin_phone_number"`
	NextOfKinRelationship          string                     `json:"next_of_kin_relationship"`
	IsCommunityOrgMember           *bool                      `json:"is_community_org_member"`
	YearsAtCommunityOrg            *int                       `json:"years_at_community_org"`
	CharacterReferenceName         string                     `json:"character_reference_name"`
	CharacterReferenceContact      string                     `json:"character_reference_contact"`
	CharacterReferenceRelationship string                     `json:"character_reference_relationship"`
	EarningGoalAmount              float64                    `json:"earning_goal_amount"`
	Skills                         []string                   `json:"skills"`
	Locations                      []string                   `json:"locations"`
	WorkingHours                   []WorkingHourInput         `json:"working_hours"`
	PastEmployers                  []PastEmployerInput        `json:"past_employers"`
	LanguageProficiencies          []LanguageProficiencyInput `json:"language_proficiencies"`
	References                     []ReferenceInput           `json:"references"`
}

var ErrInvalidSkillSelection = errors.New("select exactly one valid skill")

type WorkingHourInput struct {
	Day         string `json:"day" bson:"day"`
	OpeningTime string `json:"opening_time" bson:"opening_time"`
	ClosingTime string `json:"closing_time" bson:"closing_time"`
}

type PastEmployerInput struct {
	ID          string `json:"id" bson:"_id"`
	Name        string `json:"name" bson:"name"`
	PhoneNumber string `json:"phone_number" bson:"phone_number"`
	Location    string `json:"location" bson:"location"`
}

type LanguageProficiencyInput struct {
	ID               string `json:"id" bson:"_id"`
	Language         string `json:"language" bson:"language"`
	ProficiencyLevel string `json:"proficiency_level" bson:"proficiency_level"`
	WrittenSkills    string `json:"written_skills" bson:"written_skills"`
}

type ReferenceInput struct {
	ID          string `json:"id" bson:"_id"`
	Name        string `json:"name" bson:"name"`
	PhoneNumber string `json:"phone_number" bson:"phone_number"`
	JobTypeID   string `json:"job_type_id" bson:"job_type_id"`
	JobTitle    string `json:"job_title" bson:"job_title"`
	Email       string `json:"email" bson:"email"`
	Location    string `json:"location" bson:"location"`
}

// FullProfile is the merged user + profile view returned to clients. The
// frontend Profile type extends User, so it needs both the core identity
// fields (from the users collection) and the extension fields (from the
// profiles collection). It renders even when no profiles row exists yet.
type FullProfile struct {
	*users.User
	Avatar                         string                     `json:"avatar,omitempty"`
	IDNumber                       string                     `json:"id_number"`
	DateOfBirth                    string                     `json:"date_of_birth"`
	Gender                         string                     `json:"gender"`
	HasSocialMediaHandles          bool                       `json:"has_social_media_handles"`
	SocialMedias                   string                     `json:"social_medias"`
	HasSmartphone                  bool                       `json:"has_smartphone"`
	IsBusinessRegistered           bool                       `json:"is_business_registered"`
	BusinessName                   string                     `json:"business_name"`
	BusinessRegistrationNumber     string                     `json:"business_registration_number"`
	RecentGoodConduct              bool                       `json:"recent_good_conduct"`
	Address                        string                     `json:"address"`
	City                           string                     `json:"city"`
	State                          string                     `json:"state"`
	Zip                            string                     `json:"zip"`
	Country                        string                     `json:"country"`
	HighestEducationLevel          string                     `json:"highest_education_level"`
	Certificate                    string                     `json:"certificate"`
	Institution                    string                     `json:"institution"`
	OtherQualifications            string                     `json:"other_qualifications"`
	ProfessionalMembership         bool                       `json:"professional_membership"`
	ProfessionalBody               string                     `json:"professional_body"`
	SkillAcquisitionMode           string                     `json:"skill_acquisition_mode"`
	YearsOfExperience              int                        `json:"years_of_experience"`
	NextOfKinName                  string                     `json:"next_of_kin_name"`
	NextOfKinPhoneNumber           string                     `json:"next_of_kin_phone_number"`
	NextOfKinRelationship          string                     `json:"next_of_kin_relationship"`
	IsCommunityOrgMember           bool                       `json:"is_community_org_member"`
	YearsAtCommunityOrg            int                        `json:"years_at_community_org"`
	CharacterReferenceName         string                     `json:"character_reference_name"`
	CharacterReferenceContact      string                     `json:"character_reference_contact"`
	CharacterReferenceRelationship string                     `json:"character_reference_relationship"`
	EarningGoalAmount              float64                    `json:"earning_goal_amount"`
	Skills                         []string                   `json:"skills"`
	Locations                      []string                   `json:"locations"`
	WorkingHours                   []WorkingHourInput         `json:"working_hours"`
	PastEmployers                  []PastEmployerInput        `json:"past_employers"`
	LanguageProficiencies          []LanguageProficiencyInput `json:"language_proficiencies"`
	References                     []ReferenceInput           `json:"references"`
}

// GetFullProfile returns the authenticated user's identity merged with their
// profile extension. A missing profiles row is not an error — the user always
// has core fields, so we return those with empty extension fields.
func GetFullProfile(ctx context.Context, userID string) (*FullProfile, error) {
	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	full := &FullProfile{User: user}
	if user.AvatarID != "" {
		full.Avatar = users.AvatarPublicURL(
			user.ID,
			strconv.FormatInt(user.UpdatedAt.UnixNano(), 10),
		)
	}
	if profile, err := GetProfileByUserID(ctx, userID); err == nil {
		full.IDNumber = profile.IDNumber
		full.DateOfBirth = profile.DateOfBirth
		full.Gender = profile.Gender
		full.HasSocialMediaHandles = profile.HasSocialMediaHandles
		full.SocialMedias = profile.SocialMedias
		full.HasSmartphone = profile.HasSmartphone
		full.IsBusinessRegistered = profile.IsBusinessRegistered
		full.BusinessName = profile.BusinessName
		full.BusinessRegistrationNumber = profile.BusinessRegistrationNumber
		full.RecentGoodConduct = profile.RecentGoodConduct
		full.Address = profile.Address
		full.City = profile.City
		full.State = profile.State
		full.Zip = profile.Zip
		full.Country = profile.Country
		full.HighestEducationLevel = profile.HighestEducationLevel
		full.Certificate = profile.Certificate
		full.Institution = profile.Institution
		full.OtherQualifications = profile.OtherQualifications
		full.ProfessionalMembership = profile.ProfessionalMembership
		full.ProfessionalBody = profile.ProfessionalBody
		full.SkillAcquisitionMode = profile.SkillAcquisitionMode
		full.YearsOfExperience = profile.YearsOfExperience
		full.NextOfKinName = profile.NextOfKinName
		full.NextOfKinPhoneNumber = profile.NextOfKinPhoneNumber
		full.NextOfKinRelationship = profile.NextOfKinRelationship
		full.IsCommunityOrgMember = profile.IsCommunityOrgMember
		full.YearsAtCommunityOrg = profile.YearsAtCommunityOrg
		full.CharacterReferenceName = profile.CharacterReferenceName
		full.CharacterReferenceContact = profile.CharacterReferenceContact
		full.CharacterReferenceRelationship = profile.CharacterReferenceRelationship
		full.EarningGoalAmount = profile.EarningGoalAmount
		full.Skills = profile.Skills
		full.Locations = profile.Locations
	}

	full.WorkingHours, err = getProfileItems[WorkingHourInput]("working_hours", userID)
	if err != nil {
		return nil, err
	}
	full.PastEmployers, err = getProfileItems[PastEmployerInput]("past_employers", userID)
	if err != nil {
		return nil, err
	}
	full.LanguageProficiencies, err = getProfileItems[LanguageProficiencyInput]("language_proficiencies", userID)
	if err != nil {
		return nil, err
	}
	full.References, err = getProfileItems[ReferenceInput]("competency_references", userID)
	if err != nil {
		return nil, err
	}
	return full, nil
}

func getProfileItems[T any](collectionName, userID string) ([]T, error) {
	ctx := context.TODO()
	cursor, err := configs.MI.DB.Collection(collectionName).Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", collectionName, err)
	}
	defer cursor.Close(ctx)

	items := []T{}
	if err := cursor.All(ctx, &items); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", collectionName, err)
	}
	return items, nil
}

func buildProfileUpdates(req ProfileUpdateRequest) bson.M {
	u := bson.M{}
	setStr := func(k, v string) {
		if v != "" {
			u[k] = v
		}
	}
	setBool := func(k string, v *bool) {
		if v != nil {
			u[k] = *v
		}
	}
	setStr("id_number", req.IDNumber)
	setStr("date_of_birth", req.DateOfBirth)
	setStr("gender", req.Gender)
	setStr("social_medias", req.SocialMedias)
	setStr("business_name", req.BusinessName)
	setStr("business_registration_number", req.BusinessRegistrationNumber)
	setStr("address", req.Address)
	setStr("city", req.City)
	setStr("state", req.State)
	setStr("zip", req.Zip)
	setStr("country", req.Country)
	setStr("highest_education_level", req.HighestEducationLevel)
	setStr("certificate", req.Certificate)
	setStr("institution", req.Institution)
	setStr("other_qualifications", req.OtherQualifications)
	setStr("professional_body", req.ProfessionalBody)
	setStr("skill_acquisition_mode", req.SkillAcquisitionMode)
	setStr("next_of_kin_name", req.NextOfKinName)
	setStr("next_of_kin_phone_number", req.NextOfKinPhoneNumber)
	setStr("next_of_kin_relationship", req.NextOfKinRelationship)
	setStr("character_reference_name", req.CharacterReferenceName)
	setStr("character_reference_contact", req.CharacterReferenceContact)
	setStr("character_reference_relationship", req.CharacterReferenceRelationship)
	setBool("has_smartphone", req.HasSmartphone)
	setBool("is_business_registered", req.IsBusinessRegistered)
	setBool("recent_good_conduct", req.RecentGoodConduct)
	setBool("professional_membership", req.ProfessionalMembership)
	setBool("is_community_org_member", req.IsCommunityOrgMember)
	setBool("has_social_media_handles", req.HasSocialMediaHandles)
	if req.YearsOfExperience != nil {
		u["years_of_experience"] = *req.YearsOfExperience
	}
	if req.YearsAtCommunityOrg != nil {
		u["years_at_community_org"] = *req.YearsAtCommunityOrg
	}
	if req.EarningGoalAmount > 0 {
		u["earning_goal_amount"] = req.EarningGoalAmount
	}
	if len(req.Skills) > 0 {
		u["skills"] = req.Skills
	}
	if len(req.Locations) > 0 {
		u["locations"] = req.Locations
	}
	return u
}

func UpdateProfile(ctx context.Context, userID string, req ProfileUpdateRequest) error {
	if err := validateProfileUpdate(req); err != nil {
		return err
	}

	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	userUpdates := bson.M{}
	if req.Email != "" {
		userUpdates["email"] = req.Email
	}
	if len(userUpdates) > 0 {
		if err := users.UpdateUser(userID, userUpdates); err != nil {
			return fmt.Errorf("updating user profile fields: %w", err)
		}
	}

	profileUpdates := buildProfileUpdates(req)
	if len(profileUpdates) > 0 {
		if err := CreateOrUpdateProfile(user.ID, profileUpdates); err != nil {
			return err
		}
	}

	return updateSubResources(user.ID, req)
}

func validateProfileUpdate(req ProfileUpdateRequest) error {
	if len(req.Skills) > 1 {
		return ErrInvalidSkillSelection
	}
	if len(req.Skills) == 1 && strings.TrimSpace(req.Skills[0]) == "" {
		return ErrInvalidSkillSelection
	}
	return nil
}

func updateSubResources(userID string, req ProfileUpdateRequest) error {
	if req.SkillAcquisitionMode != "" || req.YearsOfExperience != nil {
		training := bson.M{"user_id": userID, "updated_at": time.Now()}
		if req.SkillAcquisitionMode != "" {
			training["skill_acquisition_mode"] = req.SkillAcquisitionMode
		}
		if req.YearsOfExperience != nil {
			training["years_of_experience"] = *req.YearsOfExperience
		}
		_, err := configs.MI.DB.Collection("professional_trainings").UpdateOne(
			context.TODO(),
			bson.M{"user_id": userID},
			bson.M{"$set": training},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("updating professional training: %w", err)
		}
	}

	if req.References != nil {
		documents := make([]interface{}, 0, len(req.References))
		for _, ref := range req.References {
			refData := bson.M{
				"_id":        uuid.New().String(),
				"user_id":    userID,
				"created_at": time.Now(),
				"updated_at": time.Now(),
			}
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
			documents = append(documents, refData)
		}
		if err := replaceProfileItems("competency_references", userID, documents); err != nil {
			return err
		}
	}

	if req.WorkingHours != nil {
		documents := make([]interface{}, 0, len(req.WorkingHours))
		for _, wh := range req.WorkingHours {
			documents = append(documents, bson.M{
				"_id":          uuid.New().String(),
				"user_id":      userID,
				"day":          wh.Day,
				"opening_time": wh.OpeningTime,
				"closing_time": wh.ClosingTime,
				"created_at":   time.Now(),
				"updated_at":   time.Now(),
			})
		}
		if err := replaceProfileItems("working_hours", userID, documents); err != nil {
			return err
		}
	}

	if req.PastEmployers != nil {
		documents := make([]interface{}, 0, len(req.PastEmployers))
		for _, pe := range req.PastEmployers {
			peData := bson.M{
				"_id":        uuid.New().String(),
				"user_id":    userID,
				"created_at": time.Now(),
				"updated_at": time.Now(),
			}
			if pe.Name != "" {
				peData["name"] = pe.Name
			}
			if pe.PhoneNumber != "" {
				peData["phone_number"] = pe.PhoneNumber
			}
			if pe.Location != "" {
				peData["location"] = pe.Location
			}
			documents = append(documents, peData)
		}
		if err := replaceProfileItems("past_employers", userID, documents); err != nil {
			return err
		}
	}

	if req.LanguageProficiencies != nil {
		documents := make([]interface{}, 0, len(req.LanguageProficiencies))
		for _, lp := range req.LanguageProficiencies {
			lpData := bson.M{
				"_id":        uuid.New().String(),
				"user_id":    userID,
				"created_at": time.Now(),
				"updated_at": time.Now(),
			}
			if lp.Language != "" {
				lpData["language"] = lp.Language
			}
			if lp.ProficiencyLevel != "" {
				lpData["proficiency_level"] = lp.ProficiencyLevel
			}
			if lp.WrittenSkills != "" {
				lpData["written_skills"] = lp.WrittenSkills
			}
			documents = append(documents, lpData)
		}
		if err := replaceProfileItems("language_proficiencies", userID, documents); err != nil {
			return err
		}
	}

	return nil
}

func replaceProfileItems(collectionName, userID string, documents []interface{}) error {
	ctx := context.TODO()
	collection := configs.MI.DB.Collection(collectionName)
	if _, err := collection.DeleteMany(ctx, bson.M{"user_id": userID}); err != nil {
		return fmt.Errorf("clearing %s: %w", collectionName, err)
	}
	if len(documents) == 0 {
		return nil
	}
	if _, err := collection.InsertMany(ctx, documents); err != nil {
		return fmt.Errorf("saving %s: %w", collectionName, err)
	}
	return nil
}
