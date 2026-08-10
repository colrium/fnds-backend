package assessments

import (
	"time"

	"github.com/labstack/echo/v4"
)

// MeetingCreator is the slice of the Zoho Meeting service this package needs.
// Declaring it here (rather than importing the concrete type) keeps the handler
// testable and lets a nil value mean "video calls are unavailable".
type MeetingCreator interface {
	CreateMeeting(topic string, startTime time.Time, durationMin int) (*MeetingResult, error)
}

// MeetingResult mirrors integrations.ZohoMeetingResult so this package does not
// depend on the integration's concrete types.
type MeetingResult struct {
	MeetingKey string
	JoinLink   string
	StartLink  string
}

// meetingCreator is set once at startup by RegisterArtisanRoutes. It stays nil
// when Zoho is unconfigured, and every use is nil-checked so bookings continue
// without a meeting link.
var meetingCreator MeetingCreator

// RegisterArtisanRoutes mounts the artisan surface. meetings may be nil, in
// which case oral bookings succeed without a video link.
func RegisterArtisanRoutes(g *echo.Group, meetings MeetingCreator) {
	meetingCreator = meetings
	registerArtisanRoutes(g)
}

func registerArtisanRoutes(g *echo.Group) {
	g.GET("/assessments/journey", HandleGetJourney)
	g.POST("/assessments/journey", HandleStartJourney)
	g.POST("/assessments/written/start", HandleStartWrittenAttempt)
	g.POST("/assessments/attempts/:id/answer", HandleSaveAnswer)
	g.POST("/assessments/attempts/:id/pause", HandlePauseAttempt)
	g.POST("/assessments/attempts/:id/resume", HandleResumeAttempt)
	g.POST("/assessments/attempts/:id/submit", HandleSubmitAttempt)
	g.GET("/assessments/availability", HandleListAvailability)
	g.POST("/assessments/appointments", HandleBookAppointment)
	g.GET("/assessments/appointment", HandleGetMyAppointment)
	g.POST("/assessments/appointment/cancel", HandleCancelAppointment)
	g.GET("/assessments/practical-tasks", HandleListPracticalTasks)
	g.GET("/assessments/centres", HandleListCentres)
	g.GET("/assessments/certificates/:id", HandleGetCertificate)
}

// RegisterAssessorRoutes mounts under the /assessor group, which already scopes
// these to the assessment domain, so the paths carry no second /assessments
// segment.
func RegisterAssessorRoutes(g *echo.Group) {
	g.GET("/dashboard", HandleGetDashboard)
	g.GET("/appointments", HandleListAppointments)
	g.GET("/appointments/:id", HandleGetAppointment)
	g.POST("/appointments/:id/join", HandleJoinCall)
	g.POST("/appointments/:id/notify", HandleNotifyArtisan)
	g.POST("/appointments/:id/report", HandleSubmitReport)
	g.GET("/slots", HandleListSlots)
	g.POST("/slots", HandleCreateSlot)
	g.POST("/slots/generate", HandleGenerateSlots)
	g.DELETE("/slots/:id", HandleDeleteSlot)
	g.GET("/rubric", HandleGetRubric)
	g.GET("/skills", HandleListOwnSkills)
}

func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/assessments/review-queue", HandleListReviewQueue)
	g.POST("/assessments/journeys/:id/approve", HandleApproveProfile)
	g.POST("/assessments/journeys/:id/reject", HandleRejectProfile)
	g.GET("/assessments/assessor-skills", HandleListAssessorSkills)
	g.POST("/assessments/assessor-skills", HandleGrantSkills)
	g.DELETE("/assessments/assessor-skills/:id", HandleRevokeSkill)
	g.POST("/assessments/reassign", HandleReassign)

	// Question bank CRUD
	g.GET("/assessments/questions", HandleAdminListQuestions)
	g.POST("/assessments/questions", HandleAdminCreateQuestion)
	g.PATCH("/assessments/questions/:id/approve", HandleAdminApproveQuestion)
	g.PATCH("/assessments/questions/:id/retire", HandleAdminRetireQuestion)

	// Journeys (paginated, filterable)
	g.GET("/assessments/journeys", HandleAdminListJourneys)
	g.GET("/assessments/journeys/:id", HandleAdminGetJourney)

	// Centres
	g.GET("/assessments/centres", HandleAdminListCentres)
	g.POST("/assessments/centres", HandleAdminCreateCentre)

	// Practical tasks
	g.GET("/assessments/practical-tasks", HandleAdminListPracticalTasks)
	g.POST("/assessments/practical-tasks", HandleAdminCreatePracticalTask)

	// Certificates
	g.GET("/assessments/certificates", HandleAdminListCertificates)
}
