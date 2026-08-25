package adminapi

import (
	"errors"
	"net/http"
	"strings"

	"backend/jobrequests"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// buildVacancyFilter builds the Mongo filter for the admin vacancy list,
// supporting optional status filtering.
func buildVacancyFilter(status string) bson.M {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	return filter
}

type adminVacancyListItem struct {
	jobrequests.JobVacancy
	ApplicationCount int64 `json:"application_count"`
}

func HandleListVacancies(c echo.Context) error {
	page, limit := parsePageLimit(c)
	filter := buildVacancyFilter(c.QueryParam("status"))

	list, total, err := jobrequests.ListJobVacancies(filter, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list vacancies")
	}

	vacancyIDs := make([]string, 0, len(list))
	for _, vacancy := range list {
		vacancyIDs = append(vacancyIDs, vacancy.ID)
	}
	applicationCounts, err := jobrequests.CountApplicationsByVacancyIDs(
		c.Request().Context(),
		vacancyIDs,
	)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to count vacancy applications")
	}

	items := make([]adminVacancyListItem, 0, len(list))
	for _, vacancy := range list {
		items = append(items, adminVacancyListItem{
			JobVacancy:       vacancy,
			ApplicationCount: applicationCounts[vacancy.ID],
		})
	}
	return responses.SendOK(c, Page[adminVacancyListItem]{
		Data:  items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Vacancies retrieved")
}

func HandleGetVacancy(c echo.Context) error {
	id := c.Param("id")
	vacancy, err := jobrequests.GetJobVacancyByID(id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "vacancy not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load vacancy")
	}
	return responses.SendOK(c, vacancy, "Vacancy retrieved")
}

type vacancyApplicationApplicant struct {
	ID            string `json:"id"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	FundisID      string `json:"fundis_id,omitempty"`
	ProfileStatus string `json:"profile_status"`
}

type adminVacancyApplication struct {
	jobrequests.JobApplication
	Applicant *vacancyApplicationApplicant `json:"applicant"`
}

var validApplicationStatuses = map[string]bool{
	jobrequests.ApplicationStatusPending:  true,
	jobrequests.ApplicationStatusAccepted: true,
	jobrequests.ApplicationStatusRejected: true,
}

func HandleListVacancyApplications(c echo.Context) error {
	vacancyID := strings.TrimSpace(c.Param("id"))
	if vacancyID == "" {
		return responses.SendError(c, http.StatusBadRequest, "vacancy id is required")
	}
	if _, err := jobrequests.GetJobVacancyByIDContext(c.Request().Context(), vacancyID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "vacancy not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load vacancy")
	}

	status := strings.TrimSpace(c.QueryParam("status"))
	if status != "" && !validApplicationStatuses[status] {
		return responses.SendError(c, http.StatusBadRequest, "invalid application status")
	}
	filter := bson.M{"vacancy_id": vacancyID}
	if status != "" {
		filter["status"] = status
	}

	page, limit := parsePageLimit(c)
	applications, total, err := jobrequests.ListJobApplications(
		c.Request().Context(),
		filter,
		page,
		limit,
	)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list applications")
	}

	applicantIDs := make([]string, 0, len(applications))
	seenApplicantIDs := make(map[string]bool, len(applications))
	for _, application := range applications {
		if !seenApplicantIDs[application.ApplicantID] {
			seenApplicantIDs[application.ApplicantID] = true
			applicantIDs = append(applicantIDs, application.ApplicantID)
		}
	}
	applicants, err := users.GetUsersByIDsContext(c.Request().Context(), applicantIDs)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load applicants")
	}
	applicantsByID := make(map[string]users.User, len(applicants))
	for _, applicant := range applicants {
		applicantsByID[applicant.ID] = applicant
	}

	items := make([]adminVacancyApplication, 0, len(applications))
	for _, application := range applications {
		var applicantSummary *vacancyApplicationApplicant
		if applicant, ok := applicantsByID[application.ApplicantID]; ok {
			summary := vacancyApplicationApplicant{
				ID:            applicant.ID,
				FirstName:     applicant.FirstName,
				LastName:      applicant.LastName,
				Email:         applicant.Email,
				Phone:         applicant.Phone,
				FundisID:      applicant.FundisID,
				ProfileStatus: users.EffectiveProfileStatus(&applicant),
			}
			applicantSummary = &summary
		}
		items = append(items, adminVacancyApplication{
			JobApplication: application,
			Applicant:      applicantSummary,
		})
	}

	return responses.SendOK(c, Page[adminVacancyApplication]{
		Data:  items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Applications retrieved")
}
