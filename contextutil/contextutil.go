package contextutil

import (
	"github.com/labstack/echo/v4"
)

func SetUser(c echo.Context, userID string) {
	c.Set("user_id", userID)
}

func GetUserID(c echo.Context) string {
	id, ok := c.Get("user_id").(string)
	if !ok {
		return ""
	}
	return id
}
