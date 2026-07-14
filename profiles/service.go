package profiles

import (
	"context"
	"fmt"
	"time"

	"backend/configs"
	"backend/users"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
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

// FullProfile is the merged user + profile view returned to clients. The
// frontend Profile type extends User, so it needs both the core identity
// fields (from the users collection) and the extension fields (from the
// profiles collection). It renders even when no profiles row exists yet.
type FullProfile struct {
	*users.User
	IDNumber                       string   `json:"id_number"`
	DateOfBirth                    string   `json:"date_of_birth"`
	Gender                         string   `json:"gender"`
	HasSocialMediaHandles          bool     `json:"has_social_media_handles"`
	SocialMedias                   string   `json:"social_medias"`
	HasSmartphone                  bool     `json:"has_smartphone"`
	IsBusinessRegistered           bool     `json:"is_business_registered"`
	BusinessName                   string   `json:"business_name"`
	BusinessRegistrationNumber     string   `json:"business_registration_number"`
	RecentGoodConduct              bool     `json:"recent_good_conduct"`
	Address                        string   `json:"address"`
	City                           string   `json:"city"`
	State                          string   `json:"state"`
	Zip                            string   `json:"zip"`
	Country                        string   `json:"country"`
	HighestEducationLevel          string   `json:"highest_education_level"`
	Certificate                    string   `json:"certificate"`
	Institution                    string   `json:"institution"`
	OtherQualifications            string   `json:"other_qualifications"`
	ProfessionalMembership         bool     `json:"professional_membership"`
	ProfessionalBody               string   `json:"professional_body"`
	SkillAcquisitionMode           string   `json:"skill_acquisition_mode"`
	YearsOfExperience              int      `json:"years_of_experience"`
	NextOfKinName                  string   `json:"next_of_kin_name"`
	NextOfKinPhoneNumber           string   `json:"next_of_kin_phone_number"`
	NextOfKinRelationship          string   `json:"next_of_kin_relationship"`
	IsCommunityOrgMember           bool     `json:"is_community_org_member"`
	YearsAtCommunityOrg            int      `json:"years_at_community_org"`
	CharacterReferenceName         string   `json:"character_reference_name"`
	CharacterReferenceContact      string   `json:"character_reference_contact"`
	CharacterReferenceRelationship string   `json:"character_reference_relationship"`
	EarningGoalAmount              float64  `json:"earning_goal_amount"`
	Skills                         []string `json:"skills"`
	Locations                      []string `json:"locations"`
}

// GetFullProfile returns the authenticated user's identity merged with their
// profile extension. A missing profiles row is not an error — the user always
// has core fields, so we return those with empty extension fields.
func GetFullProfile(userID string) (*FullProfile, error) {
	user, err := users.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	full := &FullProfile{User: user}
	if profile, err := GetProfileByUserID(userID); err == nil {
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
	return full, nil
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

func UpdateProfile(userID string, req ProfileUpdateRequest) error {
	user, err := users.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	userUpdates := bson.M{}
	if req.Email != "" {
		userUpdates["email"] = req.Email
	}
	if len(userUpdates) > 0 {
		_ = users.UpdateUser(userID, userUpdates)
	}

	profileUpdates := buildProfileUpdates(req)
	if len(profileUpdates) > 0 {
		profileUpdates["updated_at"] = time.Now()
		_, _ = configs.MI.DB.Collection("profiles").UpdateOne(
			context.TODO(),
			bson.M{"user_id": user.ID},
			bson.M{"$set": profileUpdates},
		)
	}

	updateSubResources(user.ID, req)

	return nil
}

func updateSubResources(userID string, req ProfileUpdateRequest) {
	if req.SkillAcquisitionMode != "" || req.YearsOfExperience != nil {
		training := bson.M{"user_id": userID, "updated_at": time.Now()}
		if req.SkillAcquisitionMode != "" {
			training["skill_acquisition_mode"] = req.SkillAcquisitionMode
		}
		if req.YearsOfExperience != nil {
			training["years_of_experience"] = *req.YearsOfExperience
		}
		_, _ = configs.MI.DB.Collection("professional_trainings").UpdateOne(
			context.TODO(),
			bson.M{"user_id": userID},
			bson.M{"$set": training},
		)
	}

	if len(req.References) > 0 {
		coll := configs.MI.DB.Collection("competency_references")
		for _, ref := range req.References {
			refData := bson.M{"user_id": userID, "updated_at": time.Now()}
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
					bson.M{"_id": ref.ID, "user_id": userID},
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
		_, _ = coll.DeleteMany(context.TODO(), bson.M{"user_id": userID})
		for _, wh := range req.WorkingHours {
			_, _ = coll.InsertOne(context.TODO(), bson.M{
				"_id":          uuid.New().String(),
				"user_id":      userID,
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
			peData := bson.M{"user_id": userID, "updated_at": time.Now()}
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
					bson.M{"_id": pe.ID, "user_id": userID},
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
			lpData := bson.M{"user_id": userID, "updated_at": time.Now()}
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
					bson.M{"_id": lp.ID, "user_id": userID},
					bson.M{"$set": lpData},
				)
			} else {
				lpData["_id"] = uuid.New().String()
				lpData["created_at"] = time.Now()
				_, _ = coll.InsertOne(context.TODO(), lpData)
			}
		}
	}
}
