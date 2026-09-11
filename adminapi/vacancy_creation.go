package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"backend/contextutil"
	"backend/jobrequests"
	"backend/responses"
	"backend/skills"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/mongo"
)

var vacancyLevels = []string{"ENTRY_LEVEL", "MID_LEVEL", "SENIOR_LEVEL", "EXECUTIVE"}
var vacancyEmploymentTypes = []string{"FULL_TIME", "PART_TIME", "CONTRACT", "INTERNSHIP"}

func HandleVacancyCreationOptions(c echo.Context) error {
	return responses.SendOK(c, struct {
		Skills          []string `json:"skills"`
		Levels          []string `json:"levels"`
		EmploymentTypes []string `json:"employment_types"`
	}{
		Skills: skills.CatalogNames(), Levels: vacancyLevels,
		EmploymentTypes: vacancyEmploymentTypes,
	}, "Vacancy options retrieved")
}

func requireVacancyOwner(c echo.Context, ownerID string) error {
	if ownerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "an existing user is required")
	}
	if _, err := users.GetUserByID(c.Request().Context(), ownerID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return echo.NewHTTPError(http.StatusNotFound, "selected user no longer exists")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load selected user")
	}
	return nil
}

func sendVacancyOwnerError(c echo.Context, err error) error {
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) {
		return responses.SendError(c, httpError.Code, fmt.Sprint(httpError.Message))
	}
	return responses.SendError(c, http.StatusInternalServerError, "failed to load selected user")
}

func HandleListUserCompanies(c echo.Context) error {
	ownerID := strings.TrimSpace(c.Param("id"))
	if err := requireVacancyOwner(c, ownerID); err != nil {
		return sendVacancyOwnerError(c, err)
	}
	companies, err := jobrequests.ListCompaniesByOwnerContext(c.Request().Context(), ownerID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load company profiles")
	}
	return responses.SendOK(c, companies, "Company profiles retrieved")
}

// Explicit input types keep ownership, audit fields, and workspace assignment
// out of the set of fields a browser can write.
type createUserCompanyRequest struct {
	Name               string `json:"name"`
	Website            string `json:"website"`
	Location           string `json:"location"`
	ContactPerson      string `json:"contact_person"`
	ContactPersonPhone string `json:"contact_person_phone"`
	ContactPersonEmail string `json:"contact_person_email"`
}

func (r *createUserCompanyRequest) normalizeAndValidate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Website = strings.TrimSpace(r.Website)
	r.Location = strings.TrimSpace(r.Location)
	r.ContactPerson = strings.TrimSpace(r.ContactPerson)
	r.ContactPersonPhone = strings.TrimSpace(r.ContactPersonPhone)
	r.ContactPersonEmail = strings.TrimSpace(r.ContactPersonEmail)
	if err := jobrequests.ValidateCompany(r.Name, r.ContactPerson, r.ContactPersonPhone); err != nil {
		return err
	}
	if r.ContactPersonEmail != "" {
		address, err := mail.ParseAddress(r.ContactPersonEmail)
		if err != nil || address.Address != r.ContactPersonEmail {
			return errors.New("enter a valid contact email address")
		}
	}
	if r.Website != "" {
		website, err := url.Parse(r.Website)
		if err != nil || website.Hostname() == "" || (website.Scheme != "http" && website.Scheme != "https") || website.User != nil {
			return errors.New("company website must be an http or https URL")
		}
	}
	return nil
}

func HandleCreateUserCompany(c echo.Context) error {
	actorID := contextutil.GetUserID(c)
	if actorID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var request createUserCompanyRequest
	if err := c.Bind(&request); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if err := request.normalizeAndValidate(); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	ownerID := strings.TrimSpace(c.Param("id"))
	if err := requireVacancyOwner(c, ownerID); err != nil {
		return sendVacancyOwnerError(c, err)
	}
	ctx := c.Request().Context()
	existing, err := jobrequests.GetCompanyByNameContext(ctx, ownerID, request.Name)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to look up company")
	}
	if existing != nil {
		return responses.SendError(c, http.StatusConflict, "this user already has a company with that name")
	}
	company := jobrequests.Company{
		OwnerID: ownerID, CreatedBy: actorID, Name: request.Name,
		Website: request.Website, Location: request.Location,
		ContactPerson: request.ContactPerson, ContactPersonPhone: request.ContactPersonPhone,
		ContactPersonEmail: request.ContactPersonEmail,
	}
	if err := jobrequests.CreateCompanyContext(ctx, &company); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create company profile")
	}
	return responses.SendCreated(c, company, "Company profile created for the selected user")
}

type createAdminVacancyRequest struct {
	OwnerID              string `json:"owner_id"`
	CompanyID            string `json:"company_id"`
	Title                string `json:"title"`
	Level                string `json:"level"`
	Positions            int    `json:"positions"`
	EmploymentType       string `json:"employment_type"`
	Experience           string `json:"experience"`
	SalaryMinimum        int64  `json:"salary_minimum"`
	SalaryMaximum        int64  `json:"salary_maximum"`
	Skill                string `json:"skill"`
	Location             string `json:"location"`
	Description          string `json:"description"`
	Responsibilities     string `json:"responsibilities"`
	ApplicationStartDate string `json:"application_start_date"`
	ApplicationEndDate   string `json:"application_end_date"`
}

func (r *createAdminVacancyRequest) normalizeAndValidate() error {
	r.OwnerID = strings.TrimSpace(r.OwnerID)
	r.CompanyID = strings.TrimSpace(r.CompanyID)
	r.Title = strings.TrimSpace(r.Title)
	r.Location = strings.TrimSpace(r.Location)
	r.Skill = strings.TrimSpace(r.Skill)
	r.Description = strings.TrimSpace(r.Description)
	r.Responsibilities = strings.TrimSpace(r.Responsibilities)
	r.Experience = strings.TrimSpace(r.Experience)
	if r.OwnerID == "" {
		return errors.New("an existing user is required")
	}
	if r.CompanyID == "" {
		return errors.New("select a saved company profile")
	}
	if err := jobrequests.ValidateCreateJobVacancy(r.Title, r.Description, r.Location, r.Skill); err != nil {
		return err
	}
	if !slices.Contains(vacancyLevels, r.Level) {
		return errors.New("select a valid job level")
	}
	if !slices.Contains(vacancyEmploymentTypes, r.EmploymentType) {
		return errors.New("select a valid employment type")
	}
	if r.Positions < 1 || r.Positions > 1000 {
		return errors.New("positions must be between 1 and 1,000")
	}
	const maxSafeInteger = 9007199254740991
	if r.SalaryMinimum < 1 || r.SalaryMaximum < r.SalaryMinimum || r.SalaryMaximum > maxSafeInteger {
		return errors.New("enter a positive salary range with maximum at least equal to minimum")
	}
	var start, end time.Time
	for _, field := range []struct {
		value *string
		date  *time.Time
		name  string
	}{
		{&r.ApplicationStartDate, &start, "application start date"},
		{&r.ApplicationEndDate, &end, "application end date"},
	} {
		*field.value = strings.TrimSpace(*field.value)
		if *field.value == "" {
			continue
		}
		date, err := time.Parse(time.DateOnly, *field.value)
		if err != nil {
			return fmt.Errorf("%s must use YYYY-MM-DD", field.name)
		}
		*field.date = date
		*field.value = date.Format(time.RFC3339)
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return errors.New("application end date cannot be before the start date")
	}
	return nil
}

type vacancyCreationHandler struct {
	notify func(context.Context, jobrequests.JobVacancy, string)
}

func (h vacancyCreationHandler) Create(c echo.Context) error {
	actorID := contextutil.GetUserID(c)
	if actorID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var request createAdminVacancyRequest
	if err := c.Bind(&request); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if err := request.normalizeAndValidate(); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	if err := requireVacancyOwner(c, request.OwnerID); err != nil {
		return sendVacancyOwnerError(c, err)
	}
	ctx := c.Request().Context()
	company, err := jobrequests.GetCompanyForOwnerContext(ctx, request.CompanyID, request.OwnerID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "company profile not found for the selected user")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load company profile")
	}
	vacancy := jobrequests.JobVacancy{
		OwnerID: request.OwnerID, CreatedBy: actorID, CompanyID: company.ID,
		WorkspaceID: company.WorkspaceID,
		Title:       request.Title, Level: request.Level, Positions: request.Positions,
		EmploymentType: request.EmploymentType, Experience: request.Experience,
		SalaryRange: fmt.Sprintf("%d-%d", request.SalaryMinimum, request.SalaryMaximum),
		Skill:       request.Skill, Location: request.Location,
		Description: request.Description, Responsibilities: request.Responsibilities,
		ApplicationStartDate: request.ApplicationStartDate, ApplicationEndDate: request.ApplicationEndDate,
		Status:      "open",
		CompanyName: company.Name, CompanyWebsite: company.Website, CompanyLocation: company.Location,
		ContactPerson: company.ContactPerson, ContactPersonPhone: company.ContactPersonPhone,
		ContactPersonEmail: company.ContactPersonEmail,
	}
	if err := jobrequests.CreateJobVacancyContext(ctx, &vacancy); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create vacancy")
	}
	if h.notify != nil {
		h.notify(ctx, vacancy, actorID)
	}
	return responses.SendCreated(c, vacancy, "Vacancy published for the selected user")
}
