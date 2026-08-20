package cvparser

// ExtractionResult represents the structured data returned by the OpenAI
// structured output when parsing a CV document.
type ExtractionResult struct {
	PersonalDetails  *PersonalDetails   `json:"personal_details,omitempty"`
	Education        *Education         `json:"education,omitempty"`
	Professional     *Professional      `json:"professional,omitempty"`
	WorkHistory      []WorkHistoryEntry `json:"work_history,omitempty"`
	Languages        []LanguageEntry    `json:"languages,omitempty"`
	References       []ReferenceEntry   `json:"references,omitempty"`
	ConfidenceScores *ConfidenceScores  `json:"confidence_scores,omitempty"`
}

// PersonalDetails contains personal information extracted from a CV.
type PersonalDetails struct {
	Address *string `json:"address"`
	City    *string `json:"city"`
	County  *string `json:"county"`
}

// Education contains educational background extracted from a CV.
type Education struct {
	HighestLevel        *string `json:"highest_level"`
	Certificate         *string `json:"certificate"`
	Institution         *string `json:"institution"`
	OtherQualifications *string `json:"other_qualifications"`
}

// Professional contains professional information extracted from a CV.
type Professional struct {
	ProfessionalBody     *string `json:"professional_body"`
	SkillAcquisitionMode *string `json:"skill_acquisition_mode"`
	YearsOfExperience    *int    `json:"years_of_experience"`
}

// WorkHistoryEntry represents a single past employer extracted from a CV.
type WorkHistoryEntry struct {
	EmployerName *string `json:"employer_name"`
	PhoneNumber  *string `json:"phone_number"`
	Location     *string `json:"location"`
}

// LanguageEntry represents a single language proficiency extracted from a CV.
type LanguageEntry struct {
	Language         *string `json:"language"`
	ProficiencyLevel *string `json:"proficiency_level"`
	WrittenSkills    *string `json:"written_skills"`
}

// ReferenceEntry represents a single reference extracted from a CV.
type ReferenceEntry struct {
	Name        *string `json:"name"`
	PhoneNumber *string `json:"phone_number"`
	JobTitle    *string `json:"job_title"`
	Email       *string `json:"email"`
	Location    *string `json:"location"`
}

// ConfidenceScores contains per-section confidence scores assigned by the
// CV parser, indicating extraction certainty for each category.
type ConfidenceScores struct {
	PersonalDetails float64 `json:"personal_details"`
	Education       float64 `json:"education"`
	Professional    float64 `json:"professional"`
	WorkHistory     float64 `json:"work_history"`
	Languages       float64 `json:"languages"`
	References      float64 `json:"references"`
}
