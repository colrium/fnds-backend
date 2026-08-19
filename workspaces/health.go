package workspaces

import (
	"net/http"
	"strings"

	"backend/configs"
	"backend/stytch"

	"github.com/labstack/echo/v4"
)

// HealthHandler serves the identity health endpoint.
type HealthHandler struct {
	adapter     *stytch.Adapter
	txSupported bool
}

// NewHealthHandler creates a HealthHandler.
func NewHealthHandler(adapter *stytch.Adapter, txSupported bool) *HealthHandler {
	return &HealthHandler{adapter: adapter, txSupported: txSupported}
}

// HandleHealth is the handler for GET /health/identity.
// Unauthenticated, no secrets, reporting:
// - employer_stytch_auth_enabled
// - legacy_employer_auth_enabled
// - transactions_supported
// - last_call from the adapter's atomic health pointer
//
// Requirements: 20.13
func (h *HealthHandler) HandleHealth(c echo.Context) error {
	stytchEnabled := strings.TrimSpace(configs.StytchEmployerAuthEnabled())
	legacyEnabled := strings.TrimSpace(configs.LegacyEmployerAuthEnabled())

	health := h.adapter.Health()

	data := map[string]any{
		"employer_stytch_auth_enabled": stytchEnabled == "true" || stytchEnabled == "1",
		"legacy_employer_auth_enabled": legacyEnabled == "true" || legacyEnabled == "1",
		"transactions_supported":       h.txSupported,
		"last_call":                    health,
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success": true,
		"data":    data,
		"message": "identity health",
	})
}
