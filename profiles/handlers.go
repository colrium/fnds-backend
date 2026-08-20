package profiles

import (
	"errors"
	"log/slog"
	"regexp"

	"backend/contextutil"
	"backend/middleware"
	"backend/responses"
	"backend/skills"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// uuidPattern detects UUIDs embedded in slugs (from the old bug that stored
// skill IDs instead of skill names in the slug).
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func containsUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

func invalidateArtisanCache(ctx echo.Context, slug string) {
	if err := middleware.InvalidateArtisanCache(ctx.Request().Context(), slug); err != nil {
		slog.ErrorContext(
			ctx.Request().Context(),
			"failed to invalidate artisan cache",
			"slug",
			slug,
			"error",
			err,
		)
	}
}

func invalidateArtisanSitemapCache(ctx echo.Context) {
	if err := middleware.InvalidateArtisanSitemapCache(ctx.Request().Context()); err != nil {
		slog.ErrorContext(
			ctx.Request().Context(),
			"failed to invalidate artisan sitemap cache",
			"error",
			err,
		)
	}
}

func HandleGetProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	profile, err := GetFullProfile(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Profile not found")
	}

	return responses.SendOK(ctx, profile, "Profile retrieved successfully")
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

	if err := UpdateProfile(ctx.Request().Context(), userID, req); err != nil {
		if errors.Is(err, ErrInvalidSkillSelection) || errors.Is(err, ErrInvalidCounty) {
			return responses.SendError(ctx, 400, err.Error())
		}
		return responses.SendError(ctx, 500, err.Error())
	}

	// Invalidate the cached public profile when an artisan edits their details.
	user, err := users.GetUserByID(ctx.Request().Context(), userID)
	if err != nil {
		slog.ErrorContext(
			ctx.Request().Context(),
			"failed to load user after profile update",
			"user_id",
			userID,
			"error",
			err,
		)
	} else if user.Slug != "" {
		invalidateArtisanCache(ctx, user.Slug)
	}

	if user != nil {
		if err := users.RevertProfileStatusToPendingIfApproved(ctx.Request().Context(), user); err != nil {
			slog.ErrorContext(
				ctx.Request().Context(),
				"failed to revert profile_status after edit",
				"user_id",
				userID,
				"error",
				err,
			)
		}
	}

	return responses.SendOK(ctx, nil, "Profile updated successfully")
}

func HandleGetProfileByUserID(ctx echo.Context) error {
	userID := ctx.Param("userId")
	if userID == "" {
		return responses.SendError(ctx, 400, "User ID is required")
	}

	profile, err := GetFullProfile(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Profile not found")
	}

	return responses.SendOK(ctx, profile, "Profile retrieved successfully")
}

// HandleTogglePublicProfile allows an artisan to opt-in or opt-out of having
// a publicly discoverable profile. When opting in, a unique slug is generated
// from the artisan's name, primary skill, and city. Opting out sets
// is_profile_public to false but retains the slug for potential re-activation.
func HandleTogglePublicProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	user, err := users.GetUserByID(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, 404, "User not found")
	}

	if user.UserType != "artisan" {
		return responses.SendError(ctx, 403, "Only artisans can toggle public profiles")
	}

	var req struct {
		IsProfilePublic bool `json:"is_profile_public"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	// Opting out: just set the flag to false, keep the slug for re-activation.
	if !req.IsProfilePublic {
		if err := users.UpdateUser(userID, bson.M{"is_profile_public": false}); err != nil {
			return responses.SendError(ctx, 500, "Failed to update profile visibility")
		}
		// Invalidate cached profile and sitemap so the private artisan is no
		// longer served or listed.
		invalidateArtisanCache(ctx, user.Slug)
		invalidateArtisanSitemapCache(ctx)
		return responses.SendOK(ctx, nil, "Profile is now private")
	}

	// Opting in: use the existing slug when possible, otherwise generate one
	// from the artisan's profile. Publishing claims the slug atomically and
	// retries with a numeric suffix if another public profile already owns it.
	slug := user.Slug
	needsSlug := slug == "" || containsUUID(slug)
	if needsSlug {
		profile, err := GetProfileByUserID(ctx.Request().Context(), userID)
		if err != nil {
			if !errors.Is(err, mongo.ErrNoDocuments) {
				return responses.SendError(ctx, 500, "Failed to generate profile slug: profile unavailable")
			}
			profile = nil
		}

		var primarySkill, city string
		if profile != nil {
			// profile.Skills stores skill IDs — resolve to human-readable name.
			if len(profile.Skills) > 0 {
				if skill, err := skills.GetSkillByID(ctx.Request().Context(), profile.Skills[0]); err == nil {
					primarySkill = skill.Name
				}
			}
			city = profile.City
		}

		slug = users.GenerateSlug(
			user.FirstName,
			user.LastName,
			primarySkill,
			city,
		)
	}

	publishedSlug, err := users.PublishWithUniqueSlug(
		ctx.Request().Context(),
		userID,
		slug,
	)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update profile visibility")
	}
	slug = publishedSlug

	// Invalidate cached public profile and sitemap so the newly public artisan
	// is immediately served and listed.
	invalidateArtisanCache(ctx, slug)
	invalidateArtisanSitemapCache(ctx)

	return responses.SendOK(ctx, map[string]interface{}{
		"slug":              slug,
		"is_profile_public": true,
	}, "Profile is now public")
}
