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

// ErrorDetail is the structured payload returned by SendErrorWithCode. It gives
// clients (like the frontend) a stable machine-readable code and optional field
// so they can steer the user into the right recovery flow.
type ErrorDetail struct {
	Code            string         `json:"code"`
	Message         string         `json:"message"`
	Field           string         `json:"field,omitempty"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
}

type StructuredErrorResponse struct {
	Success bool        `json:"success"`
	Error   ErrorDetail `json:"error"`
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

// SendErrorWithCode returns a structured error envelope with a machine-readable
// code so clients can branch on it (e.g. steer duplicate-registration into a
// sign-in flow). Pass an empty string for `field` or `suggestedAction` to omit.
func SendErrorWithCode(c echo.Context, status int, code, message, field, suggestedAction string, details map[string]any) error {
	return c.JSON(status, StructuredErrorResponse{
		Success: false,
		Error: ErrorDetail{
			Code:            code,
			Message:         message,
			Field:           field,
			SuggestedAction: suggestedAction,
			Details:         details,
		},
	})
}
