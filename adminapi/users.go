package adminapi

import (
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"

	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// buildUserFilter builds the Mongo filter for the admin user list, matching
// on exact user_type/status and a case-insensitive substring search across
// the fields an admin would recognize a user by.
func buildUserFilter(userType, status, search string) bson.M {
	filter := bson.M{}
	if userType != "" {
		filter["user_type"] = userType
	}
	if status != "" {
		filter["status"] = status
	}
	if search != "" {
		regex := bson.M{"$regex": regexp.QuoteMeta(search), "$options": "i"}
		filter["$or"] = []bson.M{
			{"first_name": regex},
			{"last_name": regex},
			{"email": regex},
			{"phone": regex},
		}
	}
	return filter
}

func HandleListUsers(c echo.Context) error {
	page, limit := parsePageLimit(c)
	filter := buildUserFilter(c.QueryParam("user_type"), c.QueryParam("status"), c.QueryParam("search"))

	list, total, err := users.ListAllUsers(filter, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list users")
	}
	if list == nil {
		list = []users.User{}
	}
	return responses.SendOK(c, Page[users.User]{
		Data:  list,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Users retrieved")
}

func HandleGetUser(c echo.Context) error {
	id := c.Param("id")
	user, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user")
	}
	return responses.SendOK(c, user, "User retrieved")
}

var validUserStatuses = map[string]bool{
	users.StatusPending:   true,
	users.StatusActive:    true,
	users.StatusSuspended: true,
}

type updateUserStatusRequest struct {
	Status string `json:"status"`
}

// SuspendHook runs after a users document is successfully set to
// users.StatusSuspended. Req 17.5 requires suspension to revoke the person's
// Stytch workspace sessions, and workspaces.MembershipService owns that
// capability — but this package cannot depend on it at construction time,
// because every handler here is a package-level function reached through
// RegisterAdminRoutes(g), which carries no dependencies.
//
// It is a hook rather than a returned error because revocation is
// best-effort: Req 17.7 says a failed revoke falls back on the membership
// check performed on every request, so a Stytch outage must not turn a
// legitimate suspension into a 500.
type SuspendHook func(userID string)

// onUserSuspended is nil until SetSuspendHook is called. It stays nil in tests
// and in deployments with Stytch employer auth disabled, where there are no
// Stytch sessions to revoke.
var onUserSuspended SuspendHook

// SetSuspendHook registers the revocation hook. Called once from main.go
// during startup, before the server begins serving.
func SetSuspendHook(hook SuspendHook) {
	onUserSuspended = hook
}

// notifySuspension fires the hook only for a suspension, and only when one is
// registered — a deployment with Stytch employer auth disabled never calls
// SetSuspendHook, so the nil case is the normal path there, not an edge case.
func notifySuspension(status string, userID string) {
	if status != users.StatusSuspended || onUserSuspended == nil {
		return
	}
	onUserSuspended(userID)
}

func HandleUpdateUserStatus(c echo.Context) error {
	id := c.Param("id")

	var req updateUserStatusRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if !validUserStatuses[req.Status] {
		return responses.SendError(c, http.StatusBadRequest, "invalid status")
	}

	if _, err := users.GetUserByID(c.Request().Context(), id); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user")
	}

	if err := users.UpdateUser(id, bson.M{"status": req.Status}); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to update user status")
	}

	// Fire before reloading the user: the suspension is already durable, and a
	// failure to read it back must not skip revocation.
	notifySuspension(req.Status, id)

	updated, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load updated user")
	}
	return responses.SendOK(c, updated, "User status updated")
}

// validAdminUserTypes intentionally excludes "admin" — granting admin access
// is sensitive enough that it is not exposed as a routine dropdown change
// alongside artisan/customer/assessor.
var validAdminUserTypes = map[string]bool{
	"artisan":  true,
	"customer": true,
	"assessor": true,
}

type updateUserTypeRequest struct {
	UserType string `json:"user_type"`
}

func HandleUpdateUserType(c echo.Context) error {
	id := c.Param("id")

	var req updateUserTypeRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if !validAdminUserTypes[req.UserType] {
		return responses.SendError(c, http.StatusBadRequest, "invalid user_type")
	}

	if _, err := users.GetUserByID(c.Request().Context(), id); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user")
	}

	if err := users.UpdateUser(id, bson.M{"user_type": req.UserType}); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to update user type")
	}

	updated, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load updated user")
	}
	return responses.SendOK(c, updated, "User type updated")
}

func HandleResendUserOtp(c echo.Context) error {
	id := c.Param("id")

	user, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user")
	}
	if user.PhoneVerified {
		return responses.SendError(c, http.StatusBadRequest, "phone is already verified")
	}

	users.IssuePhoneOTP(user.ID, user.Phone)

	return responses.SendOK(c, nil, "OTP resent")
}

type updateUserContactRequest struct {
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// duplicateContactFieldError inspects a Mongo duplicate-key error and returns
// which field (phone or email) collided, or "" if it can't tell — mirroring
// the classification auth.classifyDuplicateKeyError already does for
// registration, but scoped to this package since that helper is unexported.
func duplicateContactFieldError(err error) string {
	var writeException mongo.WriteException
	if errors.As(err, &writeException) {
		for _, we := range writeException.WriteErrors {
			msg := strings.ToLower(we.Message)
			if strings.Contains(msg, "phone") {
				return "phone"
			}
			if strings.Contains(msg, "email") {
				return "email"
			}
		}
	}
	return ""
}

func HandleUpdateUserContact(c echo.Context) error {
	id := c.Param("id")

	var req updateUserContactRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	phone := strings.TrimSpace(req.Phone)
	email := strings.TrimSpace(req.Email)
	if phone == "" && email == "" {
		return responses.SendError(c, http.StatusBadRequest, "phone or email is required")
	}
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			return responses.SendError(c, http.StatusBadRequest, "invalid email")
		}
	}

	user, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user")
	}

	update := bson.M{}
	if phone != "" && phone != user.Phone {
		update["phone"] = phone
		// An admin-driven phone change must not carry over the old number's
		// verified status — the new number has not been proven reachable.
		update["phone_verified"] = false
	}
	if email != "" && email != user.Email {
		update["email"] = email
	}
	if len(update) == 0 {
		return responses.SendOK(c, user, "No changes")
	}

	if err := users.UpdateUser(id, update); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			field := duplicateContactFieldError(err)
			switch field {
			case "phone":
				return responses.SendError(c, http.StatusConflict, "this phone number is already in use")
			case "email":
				return responses.SendError(c, http.StatusConflict, "this email is already in use")
			default:
				return responses.SendError(c, http.StatusConflict, "this phone or email is already in use")
			}
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to update contact info")
	}

	updated, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load updated user")
	}
	return responses.SendOK(c, updated, "Contact info updated")
}
