package jobrequests

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/contextutil"
	"backend/responses"
	"backend/subscriptions"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/job-requests", HandleCreateJobRequest)
	g.GET("/job-requests", HandleListJobRequests)
	g.GET("/job-requests/:id", HandleGetJobRequest)
	g.PATCH("/job-requests/:id", HandleUpdateJobRequest)
	g.POST("/job-vacancies", HandleCreateJobVacancy)
	g.GET("/job-vacancies", HandleListJobVacancies)
	g.GET("/job-vacancy", HandleGetJobVacancy)
	g.POST("/job-vacancies/update", HandleUpdateJobVacancy)
	g.POST("/job-vacancies/delete", HandleDeleteJobVacancy)
	g.POST("/job-applications", HandleCreateJobApplication)
	g.GET("/job-applications", HandleListVacancyApplications)
	g.POST("/job-applications/decision", HandleDecideJobApplication)
	g.GET("/employer/dashboard", HandleEmployerDashboard)
	g.GET("/employer/artisans", HandleEmployerBrowseArtisans)
	g.GET("/companies", HandleListCompanies)
	g.POST("/companies", HandleCreateCompany)
	g.POST("/companies/update", HandleUpdateCompany)
	g.POST("/companies/delete", HandleDeleteCompany)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.GET("/public/vacancy", HandleGetPublicJobVacancy)
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
	account, err := users.GetUserByID(userID)
	if err != nil || account.UserType != "customer" {
		return responses.SendError(ctx, http.StatusForbidden, "Only employer accounts can post vacancies")
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
	v.IsFeatured = false
	v.FeaturedAt = nil

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

	// Reuse an existing company with the same name (case-insensitive) instead of
	// creating a duplicate for this owner.
	existing, err := GetCompanyByName(userID, v.CompanyName)
	if err != nil {
		return nil, fmt.Errorf("failed to look up company")
	}
	if existing != nil {
		return existing, nil
	}

	company := &Company{
		OwnerID:            userID,
		Name:               strings.TrimSpace(v.CompanyName),
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

	existing, err := GetCompanyByName(userID, c.Name)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to look up company")
	}
	if existing != nil {
		return responses.SendError(ctx, 409, "A company with this name already exists")
	}

	c.OwnerID = userID
	c.Name = strings.TrimSpace(c.Name)
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

	// Prevent renaming into a name already used by a different company.
	existing, err := GetCompanyByName(userID, c.Name)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to look up company")
	}
	if existing != nil && existing.ID != id {
		return responses.SendError(ctx, 409, "A company with this name already exists")
	}

	updates := bson.M{
		"name":                 strings.TrimSpace(c.Name),
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
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	page := int64(1)
	limit := int64(20)

	vacancies, total, err := ListJobVacancies(employerVacancyFilter(userID), page, limit)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch job vacancies")
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"data":  vacancies,
		"total": total,
	}, "Job vacancies retrieved successfully")
}

func HandleListAvailableJobVacancies(ctx echo.Context) error {
	page := int64(1)
	limit := int64(20)

	vacancies, total, err := ListJobVacancies(publicVacancyFilter(), page, limit)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch job vacancies")
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"data":  vacancies,
		"total": total,
	}, "Job vacancies retrieved successfully")
}

func HandleGetJobVacancy(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}
	v, err := GetJobVacancyForOwner(id, userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	return responses.SendOK(ctx, v, "Job vacancy retrieved successfully")
}

func employerVacancyFilter(ownerID string) bson.M {
	return bson.M{"owner_id": ownerID}
}

func publicVacancyFilter() bson.M {
	return bson.M{"status": "open"}
}

// HandleGetPublicJobVacancy serves a single vacancy without authentication so it
// can be opened from a shared link.
func HandleGetPublicJobVacancy(ctx echo.Context) error {
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}
	v, err := GetJobVacancyByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	return responses.SendOK(ctx, v, "Job vacancy retrieved successfully")
}

func HandleUpdateJobVacancy(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}

	var v JobVacancy
	if err := ctx.Bind(&v); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := ValidateCreateJobVacancy(v.Title, v.Description, v.Location, v.Skill); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	company, err := resolveVacancyCompany(userID, &v)
	if err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	updates := bson.M{
		"title":                  v.Title,
		"level":                  v.Level,
		"positions":              v.Positions,
		"employment_type":        v.EmploymentType,
		"experience":             v.Experience,
		"salary_range":           v.SalaryRange,
		"skill":                  v.Skill,
		"location":               v.Location,
		"description":            v.Description,
		"responsibilities":       v.Responsibilities,
		"application_start_date": v.ApplicationStartDate,
		"application_end_date":   v.ApplicationEndDate,
		"company_id":             company.ID,
		"company_name":           company.Name,
		"company_website":        company.Website,
		"company_location":       company.Location,
		"contact_person":         company.ContactPerson,
		"contact_person_phone":   company.ContactPersonPhone,
		"contact_person_email":   company.ContactPersonEmail,
	}
	updated, err := UpdateJobVacancyForOwner(id, userID, updates)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update job vacancy")
	}
	if !updated {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	return responses.SendOK(ctx, nil, "Job vacancy updated successfully")
}

func HandleDeleteJobVacancy(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}

	deleted, err := DeleteJobVacancyForOwner(id, userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to delete job vacancy")
	}
	if !deleted {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	return responses.SendOK(ctx, nil, "Job vacancy deleted successfully")
}

func HandleCreateJobApplication(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	account, err := users.GetUserByID(userID)
	if err != nil || account.UserType != "artisan" {
		return responses.SendError(ctx, http.StatusForbidden, "Only worker accounts can apply for jobs")
	}

	var app JobApplication
	if err := ctx.Bind(&app); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateCreateJobApplication(app.VacancyID, app.CoverLetter); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, err.Error())
	}
	vacancy, err := GetJobVacancyByID(app.VacancyID)
	if err != nil {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	if vacancy.Status != "open" {
		return responses.SendError(ctx, http.StatusConflict, "Job vacancy is not open")
	}
	exists, err := HasJobApplication(userID, app.VacancyID)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to check job application")
	}
	if exists {
		return responses.SendError(ctx, http.StatusConflict, "You have already applied for this vacancy")
	}

	reservation, err := subscriptions.ReserveApplication(
		ctx.Request().Context(),
		userID,
		time.Now().UTC(),
	)
	if errors.Is(err, subscriptions.ErrApplicationLimitReached) {
		return responses.SendError(
			ctx,
			http.StatusPaymentRequired,
			"Monthly application limit reached. Use referral credits or purchase an unlimited plan.",
		)
	}
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to reserve application allowance")
	}

	app.ApplicantID = userID
	app.Status = ApplicationStatusPending

	if err := CreateJobApplication(&app); err != nil {
		if rollbackErr := reservation.Rollback(ctx.Request().Context()); rollbackErr != nil {
			slog.Error("failed to restore application allowance", "user_id", userID, "error", rollbackErr)
		}
		if mongo.IsDuplicateKeyError(err) {
			return responses.SendError(ctx, http.StatusConflict, "You have already applied for this vacancy")
		}
		return responses.SendError(ctx, 500, "Failed to create job application")
	}
	return responses.SendCreated(ctx, app, "Job application created successfully")
}

func HandleEmployerBrowseArtisans(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	account, err := users.GetUserByID(userID)
	if err != nil || account.UserType != "customer" {
		return responses.SendError(ctx, http.StatusForbidden, "Only employer accounts can browse workers")
	}
	allowed, err := subscriptions.HasEntitlement(
		ctx.Request().Context(),
		userID,
		subscriptions.EntitlementBrowseWorkers,
		time.Now().UTC(),
	)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to check hiring plan")
	}
	if !allowed {
		return responses.SendError(
			ctx,
			http.StatusPaymentRequired,
			"An active daily or monthly hiring plan is required",
		)
	}

	page := positiveQueryInt(ctx.QueryParam("page"), 1)
	limit := positiveQueryInt(ctx.QueryParam("limit"), 50)
	if limit > 100 {
		limit = 100
	}
	artisans, total, err := users.ListArtisans(page, limit)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to fetch workers")
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"data":  artisans,
		"total": total,
	}, "Workers retrieved successfully")
}

func positiveQueryInt(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

// HandleEmployerDashboard returns vacancy + application analytics and recent
// vacancies for the authenticated employer.
func HandleEmployerDashboard(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	vacancies, err := ListJobVacanciesByOwner(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch vacancies")
	}

	vacancyIDs := make([]string, 0, len(vacancies))
	openCount := 0
	for _, v := range vacancies {
		vacancyIDs = append(vacancyIDs, v.ID)
		if v.Status == "open" {
			openCount++
		}
	}

	statusCounts, err := CountApplicationsByStatusForVacancies(vacancyIDs)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch applications")
	}

	pending := statusCounts[ApplicationStatusPending]
	accepted := statusCounts[ApplicationStatusAccepted]
	rejected := statusCounts[ApplicationStatusRejected]

	recent := vacancies
	if len(recent) > 5 {
		recent = recent[:5]
	}

	dashboard := EmployerDashboard{
		TotalVacancies:       len(vacancies),
		OpenVacancies:        openCount,
		ClosedVacancies:      len(vacancies) - openCount,
		TotalApplications:    pending + accepted + rejected,
		PendingApplications:  pending,
		AcceptedApplications: accepted,
		RejectedApplications: rejected,
		RecentVacancies:      recent,
	}
	return responses.SendOK(ctx, dashboard, "Employer dashboard retrieved successfully")
}

// HandleListVacancyApplications returns applications for a vacancy, but only to
// the employer that owns the vacancy.
func HandleListVacancyApplications(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	vacancyID := ctx.QueryParam("vacancy_id")
	if vacancyID == "" {
		return responses.SendError(ctx, 400, "vacancy_id is required")
	}

	vacancy, err := GetJobVacancyByID(vacancyID)
	if err != nil {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	if vacancy.OwnerID != userID {
		return responses.SendError(ctx, 403, "You do not own this vacancy")
	}

	applications, err := ListJobApplicationsByVacancy(vacancyID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch applications")
	}

	// Resolve applicant profiles at read time so contact details stay current.
	applicantIDs := make([]string, 0, len(applications))
	for _, a := range applications {
		applicantIDs = append(applicantIDs, a.ApplicantID)
	}
	profiles, err := users.GetUsersByIDs(applicantIDs)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch applicants")
	}
	profileByID := make(map[string]users.User, len(profiles))
	for _, p := range profiles {
		profileByID[p.ID] = p
	}

	enriched := make([]JobApplicationWithApplicant, 0, len(applications))
	for _, a := range applications {
		item := JobApplicationWithApplicant{JobApplication: a}
		if p, ok := profileByID[a.ApplicantID]; ok {
			item.ApplicantName = strings.TrimSpace(p.FirstName + " " + p.LastName)
			item.ApplicantEmail = p.Email
			item.ApplicantPhone = p.Phone
		}
		enriched = append(enriched, item)
	}
	return responses.SendOK(ctx, enriched, "Applications retrieved successfully")
}

// HandleDecideJobApplication lets the vacancy owner accept or reject an application.
func HandleDecideJobApplication(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.QueryParam("id")
	if id == "" {
		return responses.SendError(ctx, 400, "id is required")
	}
	status := ctx.QueryParam("status")
	if !IsValidApplicationDecision(status) {
		return responses.SendError(ctx, 400, "status must be 'accepted' or 'rejected'")
	}

	app, err := GetJobApplicationByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Application not found")
	}
	vacancy, err := GetJobVacancyByID(app.VacancyID)
	if err != nil {
		return responses.SendError(ctx, 404, "Job vacancy not found")
	}
	if vacancy.OwnerID != userID {
		return responses.SendError(ctx, 403, "You do not own this vacancy")
	}

	updated, err := UpdateJobApplicationStatus(id, status)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update application")
	}
	if !updated {
		return responses.SendError(ctx, 404, "Application not found")
	}
	return responses.SendOK(ctx, nil, "Application "+status)
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
	g.GET("/job-vacancies/available", HandleListAvailableJobVacancies)
	g.POST("/job-requests/:id/accept", HandleAcceptJobRequest)
	g.POST("/job-requests/:id/complete", HandleCompleteJobRequest)
}
