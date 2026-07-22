package jobrequests

import (
	"strings"

	"backend/profiles"
)

type ApplicationReviewVacancy struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Skill      string `json:"skill"`
	Location   string `json:"location"`
	Experience string `json:"experience"`
}

type ApplicantJobFit struct {
	RequiredSkill   string `json:"required_skill"`
	VacancyLocation string `json:"vacancy_location"`
	SkillMatch      bool   `json:"skill_match"`
	LocationMatch   bool   `json:"location_match"`
}

// ApplicantProfessionalProfile contains hiring-relevant fields only. Identity
// documents, birth date, and next-of-kin details are intentionally excluded.
type ApplicantProfessionalProfile struct {
	ApplicantName          string                              `json:"applicant_name"`
	ApplicantEmail         string                              `json:"applicant_email"`
	ApplicantPhone         string                              `json:"applicant_phone"`
	Avatar                 string                              `json:"avatar"`
	City                   string                              `json:"city"`
	State                  string                              `json:"state"`
	Country                string                              `json:"country"`
	Skills                 []string                            `json:"skills"`
	PreferredLocations     []string                            `json:"preferred_locations"`
	HighestEducationLevel  string                              `json:"highest_education_level"`
	Certificate            string                              `json:"certificate"`
	Institution            string                              `json:"institution"`
	OtherQualifications    string                              `json:"other_qualifications"`
	ProfessionalMembership bool                                `json:"professional_membership"`
	ProfessionalBody       string                              `json:"professional_body"`
	SkillAcquisitionMode   string                              `json:"skill_acquisition_mode"`
	YearsOfExperience      int                                 `json:"years_of_experience"`
	RecentGoodConduct      bool                                `json:"recent_good_conduct"`
	IsBusinessRegistered   bool                                `json:"is_business_registered"`
	BusinessName           string                              `json:"business_name"`
	PastEmployers          []profiles.PastEmployerInput        `json:"past_employers"`
	LanguageProficiencies  []profiles.LanguageProficiencyInput `json:"language_proficiencies"`
	WorkingHours           []profiles.WorkingHourInput         `json:"working_hours"`
	References             []profiles.ReferenceInput           `json:"references"`
	JobFit                 ApplicantJobFit                     `json:"job_fit"`
}

type JobApplicationReview struct {
	Application         JobApplication               `json:"application"`
	Vacancy             ApplicationReviewVacancy     `json:"vacancy"`
	ProfessionalProfile ApplicantProfessionalProfile `json:"professional_profile"`
}

type ProfileLookupCatalog struct {
	SkillNames       map[string]string
	LocationNames    map[string]string
	InstitutionNames map[string]string
}

func BuildApplicantProfessionalProfile(
	full *profiles.FullProfile,
	vacancy JobVacancy,
	catalog ProfileLookupCatalog,
) ApplicantProfessionalProfile {
	skills := resolveNames(full.Skills, catalog.SkillNames)
	preferredLocations := resolveNames(full.Locations, catalog.LocationNames)
	locationCandidates := append(
		append([]string{}, preferredLocations...),
		full.City,
		full.State,
		full.Country,
	)

	return ApplicantProfessionalProfile{
		ApplicantName:          strings.TrimSpace(full.FirstName + " " + full.LastName),
		ApplicantEmail:         full.Email,
		ApplicantPhone:         full.Phone,
		Avatar:                 full.Avatar,
		City:                   full.City,
		State:                  full.State,
		Country:                full.Country,
		Skills:                 skills,
		PreferredLocations:     preferredLocations,
		HighestEducationLevel:  full.HighestEducationLevel,
		Certificate:            full.Certificate,
		Institution:            resolveName(full.Institution, catalog.InstitutionNames),
		OtherQualifications:    full.OtherQualifications,
		ProfessionalMembership: full.ProfessionalMembership,
		ProfessionalBody:       full.ProfessionalBody,
		SkillAcquisitionMode:   full.SkillAcquisitionMode,
		YearsOfExperience:      full.YearsOfExperience,
		RecentGoodConduct:      full.RecentGoodConduct,
		IsBusinessRegistered:   full.IsBusinessRegistered,
		BusinessName:           full.BusinessName,
		PastEmployers:          full.PastEmployers,
		LanguageProficiencies:  full.LanguageProficiencies,
		WorkingHours:           full.WorkingHours,
		References:             full.References,
		JobFit: ApplicantJobFit{
			RequiredSkill:   vacancy.Skill,
			VacancyLocation: vacancy.Location,
			SkillMatch:      containsFold(skills, vacancy.Skill),
			LocationMatch:   locationMatches(locationCandidates, vacancy.Location),
		},
	}
}

func CanReviewApplication(ownerID string, vacancy *JobVacancy) bool {
	return ownerID != "" && vacancy != nil && vacancy.OwnerID == ownerID
}

func resolveNames(ids []string, names map[string]string) []string {
	resolved := make([]string, 0, len(ids))
	for _, id := range ids {
		resolved = append(resolved, resolveName(id, names))
	}
	return resolved
}

func resolveName(id string, names map[string]string) string {
	if name := names[id]; name != "" {
		return name
	}
	return id
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func locationMatches(candidates []string, vacancyLocation string) bool {
	vacancy := strings.ToLower(strings.TrimSpace(vacancyLocation))
	if vacancy == "" {
		return false
	}
	for _, candidate := range candidates {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if normalized != "" &&
			(strings.Contains(vacancy, normalized) || strings.Contains(normalized, vacancy)) {
			return true
		}
	}
	return false
}
