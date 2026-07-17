package jobrequests

import (
	"fmt"

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
	g.GET("/companies", HandleListCompanies)
	g.POST("/companies", HandleCreateCompany)
	g.POST("/companies/update", HandleUpdateCompany)
	g.POST("/companies/delete", HandleDeleteCompany)
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

var allowedJobStatuses = map[string]bool{
	"pending": true, "accepted": true, "completed": true, "closed": true,
}

// buildJobRequestFilter scopes results to the caller. Only "pending" is the open,
// unassigned pool; every other case (including an empty/unknown status) is scoped
// to the artisan's own requests so a client cannot collapse the tenancy filter.
func buildJobRequestFilter(status, artisanID string) bson.M {
	filter := bson.M{}
	if status == "pending" {
		filter["status"] = "pending"
		filter["artisan_id"] = ""
		return filter
	}
	if allowedJobStatuses[status] {
		filter["status"] = status
	}
	// Non-pending (or empty/unknown) → always the artisan's own requests.
	filter["artisan_id"] = artisanID
	return filter
}

func HandleListJobRequests(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	status := ctx.QueryParam("status")
	filter := buildJobRequestFilter(status, userID)

	page := int64(1)
	limit := int64(20)
	requests, total, err := ListJobRequests(filter, page, limit)
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

	if err := ValidateCreateJobVacancy(v.Title, v.Description, v.Location, v.Skill); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	// Resolve the company: either an existing one the user owns, or auto-create
	// from the inline company fields so it can be reused for future vacancies.
	company, err := resolveVacancyCompany(userID, &v)
	if err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	v.OwnerID = userID
	v.CompanyID = company.ID
	v.CompanyName = company.Name
	v.CompanyWebsite = company.Website
	v.CompanyLocation = company.Location
	v.ContactPerson = company.ContactPerson
	v.ContactPersonPhone = company.ContactPersonPhone
	v.ContactPersonEmail = company.ContactPersonEmail
	v.Status = "open"

	if err := CreateJobVacancy(&v); err != nil {
		return responses.SendError(ctx, 500, "Failed to create job vacancy")
	}
	return responses.SendCreated(ctx, v, "Job vacancy created successfully")
}

// resolveVacancyCompany returns the company to attach to a vacancy. When the
// payload carries a company_id, it must belong to the user. Otherwise a new
// company is auto-created from the inline company fields and persisted for reuse.
func resolveVacancyCompany(userID string, v *JobVacancy) (*Company, error) {
	if v.CompanyID != "" {
		company, err := GetCompanyForOwner(v.CompanyID, userID)
		if err != nil {
			return nil, fmt.Errorf("company not found")
		}
		return company, nil
	}

	if err := ValidateCompany(v.CompanyName, v.ContactPerson, v.ContactPersonPhone); err != nil {
		return nil, err
	}

	company := &Company{
		OwnerID:            userID,
		Name:               v.CompanyName,
		Website:            v.CompanyWebsite,
		Location:           v.CompanyLocation,
		ContactPerson:      v.ContactPerson,
		ContactPersonPhone: v.ContactPersonPhone,
		ContactPersonEmail: v.ContactPersonEmail,
	}
	if err := CreateCompany(company); err != nil {
		return nil, fmt.Errorf("failed to save company")
	}
	return company, nil
}

func HandleListCompanies(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	companies, err := ListCompaniesByOwner(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch companies")
	}
	return responses.SendOK(ctx, companies, "Companies retrieved successfully")
}

func HandleCreateCompany(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var c Company
	if err := ctx.Bind(&c); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := ValidateCompany(c.Name, c.ContactPerson, c.ContactPersonPhone); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	c.OwnerID = userID
	if err := CreateCompany(&c); err != nil {
		return responses.SendError(ctx, 500, "Failed to create company")
	}
	return responses.SendCreated(ctx, c, "Company created successfully")
}

func HandleUpdateCompany(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}

	var c Company
	if err := ctx.Bind(&c); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := ValidateCompany(c.Name, c.ContactPerson, c.ContactPersonPhone); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	updates := bson.M{
		"name":                 c.Name,
		"website":              c.Website,
		"location":             c.Location,
		"contact_person":       c.ContactPerson,
		"contact_person_phone": c.ContactPersonPhone,
		"contact_person_email": c.ContactPersonEmail,
	}
	updated, err := UpdateCompanyForOwner(id, userID, updates)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update company")
	}
	if !updated {
		return responses.SendError(ctx, 404, "Company not found")
	}
	return responses.SendOK(ctx, nil, "Company updated successfully")
}

func HandleDeleteCompany(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}
	deleted, err := DeleteCompanyForOwner(id, userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to delete company")
	}
	if !deleted {
		return responses.SendError(ctx, 404, "Company not found")
	}
	return responses.SendOK(ctx, nil, "Company deleted successfully")
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

func HandleAcceptJobRequest(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.Param("id")
	claimed, err := ClaimJobRequest(id, userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to accept job request")
	}
	if !claimed {
		return responses.SendError(ctx, 400, "Job request is no longer available")
	}
	return responses.SendOK(ctx, nil, "Job request accepted")
}

func HandleCompleteJobRequest(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.Param("id")
	completed, err := CompleteJobRequest(id, userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to complete job request")
	}
	if !completed {
		return responses.SendError(ctx, 400, "Job request cannot be completed")
	}
	return responses.SendOK(ctx, nil, "Job request completed")
}

func RegisterArtisanRoutes(g *echo.Group) {
	g.POST("/job-requests/:id/accept", HandleAcceptJobRequest)
	g.POST("/job-requests/:id/complete", HandleCompleteJobRequest)
}
