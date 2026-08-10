package assessments

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"backend/responses"

	"github.com/labstack/echo/v4"
)

// CertificateVerification is the public-facing response for certificate lookups.
// It intentionally omits internal IDs and S3 keys.
type CertificateVerification struct {
	ArtisanName      string `json:"artisan_name"`
	FundisID         string `json:"fundis_id"`
	SkillID          string `json:"skill_id"`
	Grade            int    `json:"grade"`
	GradeDescriptor  string `json:"grade_descriptor"`
	VerificationCode string `json:"verification_code"`
	IssuedAt         string `json:"issued_at"`
	ExpiresAt        string `json:"expires_at"`
	Valid            bool   `json:"valid"`
}

// HandleVerifyCertificate is the public (no auth) endpoint for certificate
// verification. GET /certificates/:code
func HandleVerifyCertificate(c echo.Context) error {
	code := strings.TrimSpace(c.Param("code"))
	if code == "" {
		return responses.SendError(c, http.StatusBadRequest, "verification code is required")
	}

	cert, err := GetCertificateByCode(c.Request().Context(), code)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "certificate not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to verify certificate")
	}

	now := time.Now().UTC()
	verification := CertificateVerification{
		ArtisanName:      cert.ArtisanName,
		FundisID:         cert.FundisID,
		SkillID:          cert.SkillID,
		Grade:            cert.Grade,
		GradeDescriptor:  cert.GradeDescriptor,
		VerificationCode: cert.VerificationCode,
		IssuedAt:         cert.IssuedAt.Format(time.RFC3339),
		ExpiresAt:        cert.ExpiresAt.Format(time.RFC3339),
		Valid:            now.Before(cert.ExpiresAt),
	}

	return responses.SendOK(c, verification, "Certificate verified")
}

// RegisterCertificatePublicRoutes registers the public certificate verification
// endpoint. Called from main.go without auth middleware.
//
// The endpoint is unauthenticated and takes a guessable-shaped code, so callers
// must supply rate-limiting middleware to blunt enumeration and keep an
// unauthenticated path from consuming the database. The limiter is passed in
// rather than built here so this package stays free of echo middleware and the
// policy sits with the other public-route limits in main.go.
func RegisterCertificatePublicRoutes(g *echo.Group, limiter ...echo.MiddlewareFunc) {
	g.GET("/certificates/:code", HandleVerifyCertificate, limiter...)
}
