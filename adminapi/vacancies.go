package adminapi

import (
	"errors"
	"net/http"

	"backend/jobrequests"
	"backend/responses"

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

func HandleListVacancies(c echo.Context) error {
	page, limit := parsePageLimit(c)
	filter := buildVacancyFilter(c.QueryParam("status"))

	list, total, err := jobrequests.ListJobVacancies(filter, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list vacancies")
	}
	if list == nil {
		list = []jobrequests.JobVacancy{}
	}
	return responses.SendOK(c, Page[jobrequests.JobVacancy]{
		Data:  list,
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
