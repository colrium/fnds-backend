package profiles

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"backend/configs"
	"backend/locations"
	"backend/responses"
	"backend/reviews"
	"backend/skills"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// slugPattern validates that a slug contains only lowercase alphanumeric
// characters and hyphens, preventing injection and unnecessary DB lookups.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func sendPublicProfileDependencyError(ctx echo.Context, artisanID, dependency string, err error) error {
	slog.Error(
		"failed to build public artisan profile",
		"artisan_id", artisanID,
		"dependency", dependency,
		"error", err,
	)
	return responses.SendError(ctx, http.StatusInternalServerError, "Failed to retrieve artisan profile")
}

// WorkingHourPublic is the public view of a working hour entry (no PII).
type WorkingHourPublic struct {
	Day         string `json:"day"`
	OpeningTime string `json:"opening_time"`
	ClosingTime string `json:"closing_time"`
}

// LanguagePublic is the public view of a language proficiency entry (no PII).
type LanguagePublic struct {
	Language         string `json:"language"`
	ProficiencyLevel string `json:"proficiency_level"`
}

// PastEmployerPublic is the public view of a past employer (no phone/PII).
type PastEmployerPublic struct {
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
}

// PublicArtisanProfile is the strict allow-list DTO exposed via the public API.
// It MUST NOT contain any PII: phone, email, fcm_token, id_number,
// date_of_birth, gender, address (street), zip, next_of_kin_*, character_reference_*,
// business_registration_number, earning_goal_amount, recent_good_conduct,
// past_employers, competency_references, social_medias.
type PublicArtisanProfile struct {
	FundisID               string               `json:"fundis_id"`
	Slug                   string               `json:"slug"`
	FirstName              string               `json:"first_name"`
	LastName               string               `json:"last_name"`
	AvatarURL              string               `json:"avatar_url,omitempty"`
	BusinessName           string               `json:"business_name,omitempty"`
	City                   string               `json:"city,omitempty"`
	County                 string               `json:"county,omitempty"`
	Country                string               `json:"country,omitempty"`
	HighestEducationLevel  string               `json:"highest_education_level,omitempty"`
	Institution            string               `json:"institution,omitempty"`
	ProfessionalMembership bool                 `json:"professional_membership"`
	ProfessionalBody       string               `json:"professional_body,omitempty"`
	SkillAcquisitionMode   string               `json:"skill_acquisition_mode,omitempty"`
	YearsOfExperience      int                  `json:"years_of_experience"`
	Skills                 []string             `json:"skills"`
	Locations              []string             `json:"locations"`
	IsAvailable            bool                 `json:"is_available"`
	IsVerified             bool                 `json:"is_verified"`
	Rating                 float64              `json:"rating"`
	ReviewCount            int                  `json:"review_count"`
	CompletedJobsCount     int                  `json:"completed_jobs_count"`
	WorkingHours           []WorkingHourPublic  `json:"working_hours"`
	Languages              []LanguagePublic     `json:"languages"`
	PastEmployers          []PastEmployerPublic `json:"past_employers"`
}

// BuildPublicArtisanProfile maps a user and their profile to the public DTO.
// All dependent data (skills, locations, reviews, working hours, languages,
// past employers, completed jobs count) must be pre-fetched by the caller and
// passed in, keeping this function a pure mapper.
func BuildPublicArtisanProfile(
	user *users.User,
	profile *Profile,
	skillNames []string,
	locationNames []string,
	artisanReviews []reviews.Review,
	workingHours []WorkingHourPublic,
	languages []LanguagePublic,
	pastEmployers []PastEmployerPublic,
	completedJobsCount int,
) *PublicArtisanProfile {
	dto := &PublicArtisanProfile{
		FundisID:    user.FundisID,
		Slug:        user.Slug,
		FirstName:   user.FirstName,
		LastName:    user.LastName,
		Skills:      skillNames,
		Locations:   locationNames,
		IsAvailable: user.IsAvailable,
		IsVerified:  user.IsVerified,
	}

	// Avatar
	if user.AvatarID != "" {
		dto.AvatarURL = users.AvatarPublicURL(
			user.ID,
			strconv.FormatInt(user.UpdatedAt.UnixNano(), 10),
		)
	}

	// Profile fields (allow-listed only)
	if profile != nil {
		dto.BusinessName = profile.BusinessName
		dto.City = profile.City
		dto.County = profile.State // State maps to County in public view
		dto.Country = profile.Country
		dto.HighestEducationLevel = profile.HighestEducationLevel
		dto.Institution = profile.Institution
		dto.ProfessionalMembership = profile.ProfessionalMembership
		dto.ProfessionalBody = profile.ProfessionalBody
		dto.SkillAcquisitionMode = profile.SkillAcquisitionMode
		dto.YearsOfExperience = profile.YearsOfExperience
	}

	// Ensure non-nil slices for JSON
	if dto.Skills == nil {
		dto.Skills = []string{}
	}
	if dto.Locations == nil {
		dto.Locations = []string{}
	}

	// Reviews: compute average rating
	dto.ReviewCount = len(artisanReviews)
	if dto.ReviewCount > 0 {
		var total int
		for _, r := range artisanReviews {
			total += r.Rating
		}
		dto.Rating = float64(total) / float64(dto.ReviewCount)
	}

	dto.WorkingHours = workingHours
	if dto.WorkingHours == nil {
		dto.WorkingHours = []WorkingHourPublic{}
	}
	dto.Languages = languages
	if dto.Languages == nil {
		dto.Languages = []LanguagePublic{}
	}
	dto.PastEmployers = pastEmployers
	if dto.PastEmployers == nil {
		dto.PastEmployers = []PastEmployerPublic{}
	}
	dto.CompletedJobsCount = completedJobsCount

	return dto
}

func getPublicWorkingHours(ctx context.Context, userID string) ([]WorkingHourPublic, error) {
	cursor, err := configs.MI.DB.Collection("working_hours").Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var items []WorkingHourInput
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	result := make([]WorkingHourPublic, 0, len(items))
	for _, item := range items {
		result = append(result, WorkingHourPublic(item))
	}
	return result, nil
}

func getPublicLanguages(ctx context.Context, userID string) ([]LanguagePublic, error) {
	cursor, err := configs.MI.DB.Collection("language_proficiencies").Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var items []LanguageProficiencyInput
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	result := make([]LanguagePublic, 0, len(items))
	for _, item := range items {
		result = append(result, LanguagePublic{
			Language:         item.Language,
			ProficiencyLevel: item.ProficiencyLevel,
		})
	}
	return result, nil
}

func getPublicPastEmployers(ctx context.Context, userID string) ([]PastEmployerPublic, error) {
	cursor, err := configs.MI.DB.Collection("past_employers").Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var items []PastEmployerInput
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	result := make([]PastEmployerPublic, 0, len(items))
	for _, item := range items {
		if item.Name == "" {
			continue
		}
		result = append(result, PastEmployerPublic{
			Name:     item.Name,
			Location: item.Location,
		})
	}
	return result, nil
}

func countCompletedJobs(ctx context.Context, artisanID string) (int, error) {
	count, err := configs.MI.DB.Collection("job_requests").CountDocuments(ctx, bson.M{
		"artisan_id": artisanID,
		"status":     "completed",
	})
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

// publicArtisanProfileDependencies is the request-scoped seam used by the
// public profile handler. Production dependencies are constructed per request,
// so tests can replace them without mutable package globals.
type publicArtisanProfileDependencies struct {
	getPublicUserByFundisID func(context.Context, string) (*users.User, error)
	getUserBySlug           func(context.Context, string) (*users.User, error)
	getProfileByUserID      func(context.Context, string) (*Profile, error)
	getSkillNamesByIDs      func(context.Context, []string) ([]string, error)
	getLocationNamesByIDs   func(context.Context, []string) ([]string, error)
	listReviews             func(context.Context, bson.M) ([]reviews.Review, error)
	getWorkingHours         func(context.Context, string) ([]WorkingHourPublic, error)
	getLanguages            func(context.Context, string) ([]LanguagePublic, error)
	getPastEmployers        func(context.Context, string) ([]PastEmployerPublic, error)
	countCompletedJobs      func(context.Context, string) (int, error)
}

func newPublicArtisanProfileDependencies() publicArtisanProfileDependencies {
	return publicArtisanProfileDependencies{
		getPublicUserByFundisID: users.GetPublicUserByFundisID,
		getUserBySlug:           users.GetUserBySlug,
		getProfileByUserID:      GetProfileByUserID,
		getSkillNamesByIDs:      skills.GetSkillNamesByIDs,
		getLocationNamesByIDs:   locations.GetLocationNamesByIDs,
		listReviews:             reviews.ListReviewsContext,
		getWorkingHours:         getPublicWorkingHours,
		getLanguages:            getPublicLanguages,
		getPastEmployers:        getPublicPastEmployers,
		countCompletedJobs:      countCompletedJobs,
	}
}

// HandleGetPublicArtisanProfile is the JSON API endpoint for retrieving a
// public artisan profile by slug.
func HandleGetPublicArtisanProfile(ctx echo.Context) error {
	return handleGetPublicArtisanProfile(ctx, newPublicArtisanProfileDependencies())
}

func handleGetPublicArtisanProfile(ctx echo.Context, deps publicArtisanProfileDependencies) error {
	reqCtx := ctx.Request().Context()
	slug := ctx.Param("slug")
	if slug == "" || !slugPattern.MatchString(slug) {
		// If it doesn't match slug format, it might be a fundis_id — try to
		// resolve it and redirect the caller to the canonical slug URL.
		if slug != "" {
			user, err := deps.getPublicUserByFundisID(reqCtx, slug)
			if err == nil && user.Slug != "" {
				return ctx.Redirect(http.StatusFound,
					"/api/v1/artisans/"+user.Slug)
			}
		}
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid or missing slug")
	}

	user, err := deps.getUserBySlug(reqCtx, slug)
	if err != nil {
		return responses.SendError(ctx, http.StatusNotFound, "Artisan not found")
	}

	profile, err := deps.getProfileByUserID(reqCtx, user.ID)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return sendPublicProfileDependencyError(ctx, user.ID, "profile", err)
		}
		profile = nil
	}

	// Fetch dependent data at the handler level (not inside the mapper).
	skillNames := []string{}
	locationNames := []string{}
	if profile != nil {
		skillNames, err = deps.getSkillNamesByIDs(reqCtx, profile.Skills)
		if err != nil {
			return sendPublicProfileDependencyError(ctx, user.ID, "skills", err)
		}

		locationNames, err = deps.getLocationNamesByIDs(reqCtx, profile.Locations)
		if err != nil {
			return sendPublicProfileDependencyError(ctx, user.ID, "locations", err)
		}
	}

	artisanReviews, err := deps.listReviews(reqCtx, bson.M{"artisan_id": user.ID})
	if err != nil {
		return sendPublicProfileDependencyError(ctx, user.ID, "reviews", err)
	}
	if artisanReviews == nil {
		artisanReviews = []reviews.Review{}
	}

	workingHours, err := deps.getWorkingHours(reqCtx, user.ID)
	if err != nil {
		return sendPublicProfileDependencyError(ctx, user.ID, "working_hours", err)
	}
	if workingHours == nil {
		workingHours = []WorkingHourPublic{}
	}

	languages, err := deps.getLanguages(reqCtx, user.ID)
	if err != nil {
		return sendPublicProfileDependencyError(ctx, user.ID, "languages", err)
	}
	if languages == nil {
		languages = []LanguagePublic{}
	}

	pastEmployers, err := deps.getPastEmployers(reqCtx, user.ID)
	if err != nil {
		return sendPublicProfileDependencyError(ctx, user.ID, "past_employers", err)
	}
	if pastEmployers == nil {
		pastEmployers = []PastEmployerPublic{}
	}

	completedJobsCount, err := deps.countCompletedJobs(reqCtx, user.ID)
	if err != nil {
		return sendPublicProfileDependencyError(ctx, user.ID, "completed_jobs", err)
	}

	dto := BuildPublicArtisanProfile(
		user,
		profile,
		skillNames,
		locationNames,
		artisanReviews,
		workingHours,
		languages,
		pastEmployers,
		completedJobsCount,
	)

	ctx.Response().Header().Set("Cache-Control", "public, s-maxage=300, max-age=60")
	return responses.SendOK(ctx, dto, "Artisan profile retrieved successfully")
}
