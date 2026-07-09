package responses

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type SuccessResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message"`
}

type ErrorResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func SendOK(c echo.Context, data interface{}, message string) error {
	return c.JSON(http.StatusOK, SuccessResponse{
		Success: true,
		Data:    data,
		Message: message,
	})
}

func SendCreated(c echo.Context, data interface{}, message string) error {
	return c.JSON(http.StatusCreated, SuccessResponse{
		Success: true,
		Data:    data,
		Message: message,
	})
}

func SendError(c echo.Context, status int, msg string) error {
	return c.JSON(status, ErrorResponse{
		Success: false,
		Message: msg,
	})
}
