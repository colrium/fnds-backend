package adminapi

import (
	"errors"
	"net/http"
	"regexp"

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

	updated, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load updated user")
	}
	return responses.SendOK(c, updated, "User status updated")
}
