package jobrequests

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"backend/profiles"
	"backend/skills"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type profileByUserIDLookup func(context.Context, string) (*profiles.Profile, error)

func listAvailableJobVacanciesForWorker(
	ctx context.Context,
	userID string,
	page int64,
	limit int64,
) ([]JobVacancy, int64, error) {
	return listAvailableJobVacanciesForWorkerWithProfileLookup(
		ctx,
		userID,
		page,
		limit,
		profiles.GetProfileByUserID,
	)
}

func listAvailableJobVacanciesForWorkerWithProfileLookup(
	ctx context.Context,
	userID string,
	page int64,
	limit int64,
	getProfileByUserID profileByUserIDLookup,
) ([]JobVacancy, int64, error) {
	profile, err := getProfileByUserID(ctx, userID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return []JobVacancy{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("loading worker profile: %w", err)
	}

	skillList, err := skills.GetAllSkills()
	if err != nil {
		return nil, 0, fmt.Errorf("loading skills: %w", err)
	}

	filter, _, matched := workerVacancyFilter(profile.Skills, skillList)
	if !matched {
		return []JobVacancy{}, 0, nil
	}
	return ListJobVacancies(filter, page, limit)
}

func workerVacancyFilter(
	profileSkills []string,
	skillList []skills.Skill,
) (bson.M, string, bool) {
	if len(profileSkills) != 1 {
		return nil, "", false
	}

	selected := strings.TrimSpace(profileSkills[0])
	if selected == "" {
		return nil, "", false
	}

	for _, skill := range skillList {
		isSelectedID := skill.ID == selected
		isLegacyName := strings.EqualFold(skill.Name, selected)
		if !isSelectedID && !isLegacyName {
			continue
		}

		name := strings.TrimSpace(skill.Name)
		if name == "" {
			return nil, "", false
		}
		return bson.M{
			"status": "open",
			"skill": bson.M{
				"$regex":   "^" + regexp.QuoteMeta(name) + "$",
				"$options": "i",
			},
		}, name, true
	}

	return nil, "", false
}
