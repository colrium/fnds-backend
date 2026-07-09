package users

import (
	"backend/configs"
	"backend/contextutil"
	"backend/responses"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func HandleSendOtp(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid user")
	}

	if user.PhoneVerified {
		return responses.SendError(ctx, 400, "OTP already verified")
	}

	otp := "123456"
	_ = UpdateUser(userID, bson.M{"otp": otp})

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleVerifyOtp(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	otp := ctx.FormValue("otp")
	if otp == "" {
		return responses.SendError(ctx, 400, "otp is required")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid user")
	}

	if user.PhoneVerified {
		return responses.SendError(ctx, 400, "OTP already verified")
	}

	if user.Otp != otp {
		return responses.SendError(ctx, 400, "Invalid OTP")
	}

	_ = UpdateUser(userID, bson.M{"otp": "", "phone_verified": true})

	return responses.SendOK(ctx, nil, "OTP verified successfully")
}

func HandleResendOtp(ctx echo.Context) error {
	return HandleSendOtp(ctx)
}

func HandleForgotPassword(ctx echo.Context) error {
	phone := ctx.FormValue("phone")
	userType := ctx.FormValue("user_type")

	if phone == "" {
		return responses.SendError(ctx, 400, "phone is required")
	}
	if userType == "" {
		return responses.SendError(ctx, 400, "user_type is required")
	}

	user, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return responses.SendError(ctx, 400, "Phone number does not exist")
	}

	otp := "123456"
	_ = UpdateUser(user.ID, bson.M{"otp": otp})

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleResetPassword(ctx echo.Context) error {
	phone := ctx.Param("phone")
	otp := ctx.FormValue("otp")
	password := ctx.FormValue("password")
	confirmPassword := ctx.FormValue("confirm_password")
	userType := ctx.FormValue("user_type")

	if phone == "" || otp == "" || password == "" || confirmPassword == "" || userType == "" {
		return responses.SendError(ctx, 400, "All fields are required")
	}

	if password != confirmPassword {
		return responses.SendError(ctx, 400, "Passwords do not match")
	}

	user, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid phone number")
	}

	if user.Otp != otp {
		return responses.SendError(ctx, 400, "Invalid OTP")
	}

	_ = UpdateUser(user.ID, bson.M{"password": []byte(password), "otp": "", "phone_verified": true})

	return responses.SendOK(ctx, nil, "Password reset successfully")
}

func HandleUpdateToken(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	token := ctx.FormValue("token")
	if token == "" {
		return responses.SendError(ctx, 400, "token is required")
	}

	_ = UpdateUser(userID, bson.M{"fcm_token": token})

	return responses.SendOK(ctx, nil, "FCM token updated")
}

type ProfileUpdateRequest struct {
	FirstName                 string                `json:"first_name"`
	LastName                  string                `json:"last_name"`
	IDNumber                  string                `json:"id_number"`
	Locations                 []string              `json:"locations"`
	Skills                    []string              `json:"skills"`
	DateOfBirth               string                `json:"date_of_birth"`
	Gender                    string                `json:"gender"`
	HasSocialMediaHandles     *bool                 `json:"has_social_media_handles"`
	SocialMedias              string                `json:"social_medias"`
	HasSmartphone             *bool                 `json:"has_smartphone"`
	IsBusinessRegistered      *bool                 `json:"is_business_registered"`
	BusinessName              string                `json:"business_name"`
	BusinessRegistrationNumber string              `json:"business_registration_number"`
	RecentGoodConduct         *bool                 `json:"recent_good_conduct"`
	Address                   string                `json:"address"`
	City                      string                `json:"city"`
	State                     string                `json:"state"`
	Zip                       string                `json:"zip"`
	Country                   string                `json:"country"`
	SkillAcquisitionMode      string                `json:"skill_acquisition_mode"`
	YearsOfExperience         int                   `json:"years_of_experience"`
	NextOfKinName             string                `json:"next_of_kin_name"`
	NextOfKinPhoneNumber      string                `json:"next_of_kin_phone_number"`
	NextOfKinRelationship     string                `json:"next_of_kin_relationship"`
	IsCommunityOrgMember      *bool                 `json:"is_community_org_member"`
	YearsAtCommunityOrg       int                   `json:"years_at_community_org"`
	CharacterReferenceName    string                `json:"character_reference_name"`
	CharacterReferenceContact string                `json:"character_reference_contact"`
	CharacterReferenceRelationship string           `json:"character_reference_relationship"`
	EarningGoalAmount         float64               `json:"earning_goal_amount"`
	WorkingHours              []WorkingHourInput    `json:"working_hours"`
	PastEmployers             []PastEmployerInput   `json:"past_employers"`
	LanguageProficiencies     []LanguageProficiencyInput `json:"language_proficiencies"`
	References                []ReferenceInput      `json:"references"`
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

func HandleGetProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 404, "User not found")
	}

	return responses.SendOK(ctx, user, "User fetched successfully")
}

func HandleUpdateProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req ProfileUpdateRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 404, "User not found")
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
	if req.State != "" {
		profileUpdates["state"] = req.State
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
			ctx.Request().Context(),
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
			ctx.Request().Context(),
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
					ctx.Request().Context(),
					bson.M{"_id": ref.ID, "user_id": user.ID},
					bson.M{"$set": refData},
				)
			} else {
				refData["_id"] = configUUID()
				refData["created_at"] = time.Now()
				_, _ = coll.InsertOne(ctx.Request().Context(), refData)
			}
		}
	}

	if req.WorkingHours != nil {
		coll := configs.MI.DB.Collection("working_hours")
		_, _ = coll.DeleteMany(ctx.Request().Context(), bson.M{"user_id": user.ID})
		for _, wh := range req.WorkingHours {
			_, _ = coll.InsertOne(ctx.Request().Context(), bson.M{
				"_id":          configUUID(),
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
					ctx.Request().Context(),
					bson.M{"_id": pe.ID, "user_id": user.ID},
					bson.M{"$set": peData},
				)
			} else {
				peData["_id"] = configUUID()
				peData["created_at"] = time.Now()
				_, _ = coll.InsertOne(ctx.Request().Context(), peData)
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
					ctx.Request().Context(),
					bson.M{"_id": lp.ID, "user_id": user.ID},
					bson.M{"$set": lpData},
				)
			} else {
				lpData["_id"] = configUUID()
				lpData["created_at"] = time.Now()
				_, _ = coll.InsertOne(ctx.Request().Context(), lpData)
			}
		}
	}

	return responses.SendOK(ctx, nil, "Profile updated successfully")
}

func configUUID() string {
	return uuid.New().String()
}
