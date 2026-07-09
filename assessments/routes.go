package assessments

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/assessments/:assessmentId/responses", HandleSubmitResponse)
	g.GET("/appointments", HandleListAppointments)
	g.POST("/appointments", HandleCreateAppointment)
	g.PATCH("/appointments/:id", HandleUpdateAppointment)
	g.GET("/time-slots", HandleListTimeSlots)
	g.POST("/time-slots", HandleCreateTimeSlot)
}

func HandleGetAssessments(ctx echo.Context) error {
	assessmentList, err := GetAllAssessments()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch assessments")
	}
	return responses.SendOK(ctx, assessmentList, "Assessments retrieved successfully")
}

func HandleGetAssessmentTypes(ctx echo.Context) error {
	types, err := GetAllAssessmentTypes()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch assessment types")
	}
	return responses.SendOK(ctx, types, "Assessment types retrieved successfully")
}

func HandleGetQuestions(ctx echo.Context) error {
	assessmentID := ctx.Param("assessmentId")
	questions, err := GetAssessmentQuestions(assessmentID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch questions")
	}
	return responses.SendOK(ctx, questions, "Questions retrieved successfully")
}

func HandleSubmitResponse(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var resp AssessmentResponse
	if err := ctx.Bind(&resp); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	resp.UserID = userID
	resp.AssessmentID = ctx.Param("assessmentId")

	if err := CreateAssessmentResponse(&resp); err != nil {
		return responses.SendError(ctx, 500, "Failed to submit response")
	}
	return responses.SendCreated(ctx, resp, "Response submitted successfully")
}

func HandleListAppointments(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	appointments, err := ListAppointments(bson.M{"user_id": userID})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch appointments")
	}
	return responses.SendOK(ctx, appointments, "Appointments retrieved successfully")
}

func HandleCreateAppointment(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var appt Appointment
	if err := ctx.Bind(&appt); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	appt.UserID = userID
	appt.Status = "scheduled"

	if err := CreateAppointment(&appt); err != nil {
		return responses.SendError(ctx, 500, "Failed to create appointment")
	}
	return responses.SendCreated(ctx, appt, "Appointment created successfully")
}

func HandleUpdateAppointment(ctx echo.Context) error {
	id := ctx.Param("id")
	var updates map[string]interface{}
	if err := ctx.Bind(&updates); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateAppointment(id, updates); err != nil {
		return responses.SendError(ctx, 500, "Failed to update appointment")
	}
	return responses.SendOK(ctx, nil, "Appointment updated successfully")
}

func HandleListTimeSlots(ctx echo.Context) error {
	slots, err := ListTimeSlots(bson.M{})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch time slots")
	}
	return responses.SendOK(ctx, slots, "Time slots retrieved successfully")
}

func HandleCreateTimeSlot(ctx echo.Context) error {
	var slot TimeSlot
	if err := ctx.Bind(&slot); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := CreateTimeSlot(&slot); err != nil {
		return responses.SendError(ctx, 500, "Failed to create time slot")
	}
	return responses.SendCreated(ctx, slot, "Time slot created successfully")
}
