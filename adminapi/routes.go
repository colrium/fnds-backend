package adminapi

import (
	"backend/jobrequests"

	"github.com/labstack/echo/v4"
)

// RegisterAdminRoutes registers the admin data-management endpoints on the
// existing /api/v1/admin group (JWTAuth + IsAdmin, wired in main.go).
func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/users", HandleListUsers)
	g.GET("/users/:id", HandleGetUser)
	g.GET("/users/:id/companies", HandleListUserCompanies)
	g.POST("/users/:id/companies", HandleCreateUserCompany)
	g.PATCH("/users/:id/status", HandleUpdateUserStatus)
	g.PATCH("/users/:id/user-type", HandleUpdateUserType)
	g.POST("/users/:id/resend-otp", HandleResendUserOtp)
	g.PATCH("/users/:id/contact", HandleUpdateUserContact)
	g.GET("/artisans", HandleListArtisans)
	g.GET("/artisans/:id", HandleGetArtisan)
	g.PATCH("/artisans/:id/verification", HandleUpdateArtisanVerification)
	g.GET("/stats", HandleGetStats)
	g.GET("/profile-reviews", HandleListProfileReviews)
	g.GET("/profile-reviews/:user_id", HandleGetProfileReview)
	g.POST("/profile-reviews/:user_id/approve", HandleApproveProfileReview)
	g.POST("/profile-reviews/:user_id/reject", HandleRejectProfileReview)
	g.GET("/vacancies", HandleListVacancies)
	g.GET("/vacancies/options", HandleVacancyCreationOptions)
	g.POST("/vacancies", vacancyCreationHandler{notify: jobrequests.NotifyVacancyPublished}.Create)
	g.GET("/vacancies/:id/applications", HandleListVacancyApplications)
	g.GET("/vacancies/:id", HandleGetVacancy)
}
