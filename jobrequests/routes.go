package jobrequests

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/job-requests", HandleCreateJobRequest)
	g.GET("/job-requests", HandleListJobRequests)
	g.GET("/job-requests/:id", HandleGetJobRequest)
	g.PATCH("/job-requests/:id", HandleUpdateJobRequest)
	g.POST("/job-vacancies", HandleCreateJobVacancy)
	g.GET("/job-vacancies", HandleListJobVacancies)
	g.POST("/job-applications", HandleCreateJobApplication)
}

func HandleCreateJobRequest(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var jr JobRequest
	if err := ctx.Bind(&jr); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	jr.CustomerID = userID
	jr.Status = "pending"

	if err := CreateJobRequest(&jr); err != nil {
		return responses.SendError(ctx, 500, "Failed to create job request")
	}
	return responses.SendCreated(ctx, jr, "Job request created successfully")
}

func HandleListJobRequests(ctx echo.Context) error {
	page := int64(1)
	limit := int64(20)

	requests, total, err := ListJobRequests(bson.M{}, page, limit)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch job requests")
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"data":  requests,
		"total": total,
	}, "Job requests retrieved successfully")
}

func HandleGetJobRequest(ctx echo.Context) error {
	id := ctx.Param("id")
	jr, err := GetJobRequestByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Job request not found")
	}
	return responses.SendOK(ctx, jr, "Job request retrieved successfully")
}

func HandleUpdateJobRequest(ctx echo.Context) error {
	id := ctx.Param("id")
	var updates map[string]interface{}
	if err := ctx.Bind(&updates); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateJobRequest(id, updates); err != nil {
		return responses.SendError(ctx, 500, "Failed to update job request")
	}
	return responses.SendOK(ctx, nil, "Job request updated successfully")
}

func HandleCreateJobVacancy(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var v JobVacancy
	if err := ctx.Bind(&v); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	v.CompanyID = userID
	v.Status = "open"

	if err := CreateJobVacancy(&v); err != nil {
		return responses.SendError(ctx, 500, "Failed to create job vacancy")
	}
	return responses.SendCreated(ctx, v, "Job vacancy created successfully")
}

func HandleListJobVacancies(ctx echo.Context) error {
	page := int64(1)
	limit := int64(20)

	vacancies, total, err := ListJobVacancies(bson.M{}, page, limit)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch job vacancies")
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"data":  vacancies,
		"total": total,
	}, "Job vacancies retrieved successfully")
}

func HandleCreateJobApplication(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var app JobApplication
	if err := ctx.Bind(&app); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	app.ApplicantID = userID
	app.Status = "pending"

	if err := CreateJobApplication(&app); err != nil {
		return responses.SendError(ctx, 500, "Failed to create job application")
	}
	return responses.SendCreated(ctx, app, "Job application created successfully")
}
