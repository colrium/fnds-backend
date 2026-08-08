package cvparser

import (
	"math"
	"sort"
	"strings"
)

// maxCollectionItems is the maximum number of items allowed in any collection
// field of the autofill payload.
const maxCollectionItems = 10

// AutofillPayload represents the structured autofill data returned to the
// frontend after CV parsing. Each field includes a confidence score indicating
// extraction certainty.
type AutofillPayload struct {
	// Scalar fields with confidence
	Address               *FieldWithConfidence        `json:"address,omitempty"`
	City                  *FieldWithConfidence        `json:"city,omitempty"`
	State                 *FieldWithConfidence        `json:"state,omitempty"`
	HighestEducationLevel *FieldWithConfidence        `json:"highest_education_level,omitempty"`
	Certificate           *FieldWithConfidence        `json:"certificate,omitempty"`
	Institution           *FieldWithConfidence        `json:"institution,omitempty"`
	OtherQualifications   *FieldWithConfidence        `json:"other_qualifications,omitempty"`
	ProfessionalBody      *FieldWithConfidence        `json:"professional_body,omitempty"`
	SkillAcquisitionMode  *FieldWithConfidence        `json:"skill_acquisition_mode,omitempty"`
	YearsOfExperience     *NumericFieldWithConfidence `json:"years_of_experience,omitempty"`

	// Collection fields with per-item confidence
	PastEmployers         []PastEmployerWithConfidence        `json:"past_employers,omitempty"`
	LanguageProficiencies []LanguageProficiencyWithConfidence `json:"language_proficiencies,omitempty"`
	References            []ReferenceWithConfidence           `json:"references,omitempty"`
}

// FieldWithConfidence pairs a string value with a confidence score.
type FieldWithConfidence struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// NumericFieldWithConfidence pairs an integer value with a confidence score.
type NumericFieldWithConfidence struct {
	Value      int     `json:"value"`
	Confidence float64 `json:"confidence"`
}

// PastEmployerWithConfidence represents a past employer entry with
// per-field and overall confidence scores. Per-field pointers are nil when
// the source CV had no value, mirroring the scalar fields on AutofillPayload.
type PastEmployerWithConfidence struct {
	Name        *FieldWithConfidence `json:"name,omitempty"`
	PhoneNumber *FieldWithConfidence `json:"phone_number,omitempty"`
	Location    *FieldWithConfidence `json:"location,omitempty"`
	Confidence  float64              `json:"confidence"`
}

// LanguageProficiencyWithConfidence represents a language proficiency entry
// with per-field and overall confidence scores. Per-field pointers are nil
// when the source CV had no value, mirroring the scalar fields on
// AutofillPayload.
type LanguageProficiencyWithConfidence struct {
	Language         *FieldWithConfidence `json:"language,omitempty"`
	ProficiencyLevel *FieldWithConfidence `json:"proficiency_level,omitempty"`
	WrittenSkills    *FieldWithConfidence `json:"written_skills,omitempty"`
	Confidence       float64              `json:"confidence"`
}

// ReferenceWithConfidence represents a reference entry with per-field and
// overall confidence scores. Per-field pointers are nil when the source CV
// had no value, mirroring the scalar fields on AutofillPayload.
type ReferenceWithConfidence struct {
	Name        *FieldWithConfidence `json:"name,omitempty"`
	PhoneNumber *FieldWithConfidence `json:"phone_number,omitempty"`
	JobTitle    *FieldWithConfidence `json:"job_title,omitempty"`
	Email       *FieldWithConfidence `json:"email,omitempty"`
	Location    *FieldWithConfidence `json:"location,omitempty"`
	Confidence  float64              `json:"confidence"`
}

// Mapper maps ExtractionResult to AutofillPayload, enforcing field validation,
// collection caps, and confidence score bounds.
type Mapper struct{}

// NewMapper creates a new Mapper instance.
func NewMapper() *Mapper {
	return &Mapper{}
}

// MapToAutofill converts an ExtractionResult into an AutofillPayload.
// It enforces the following invariants:
//   - Only known profile fields are included
//   - Nil/empty string pointer values are omitted
//   - Collections are capped at 10 items, sorted by descending confidence
//   - All confidence scores are clamped to [0.0, 1.0] and rounded to 2 decimal places
func (m *Mapper) MapToAutofill(result *ExtractionResult) *AutofillPayload {
	if result == nil {
		return &AutofillPayload{}
	}

	payload := &AutofillPayload{}

	// Get section confidence scores (defaulting to 0 if nil)
	scores := getConfidenceScores(result.ConfidenceScores)

	// Map personal details
	if result.PersonalDetails != nil {
		conf := scores.PersonalDetails
		payload.Address = mapStringField(result.PersonalDetails.Address, conf)
		payload.City = mapStringField(result.PersonalDetails.City, conf)
		payload.State = mapStringField(result.PersonalDetails.State, conf)
	}

	// Map education
	if result.Education != nil {
		conf := scores.Education
		payload.HighestEducationLevel = mapStringField(result.Education.HighestLevel, conf)
		payload.Certificate = mapStringField(result.Education.Certificate, conf)
		payload.Institution = mapStringField(result.Education.Institution, conf)
		payload.OtherQualifications = mapStringField(result.Education.OtherQualifications, conf)
	}

	// Map professional
	if result.Professional != nil {
		conf := scores.Professional
		payload.ProfessionalBody = mapStringField(result.Professional.ProfessionalBody, conf)
		payload.SkillAcquisitionMode = mapStringField(result.Professional.SkillAcquisitionMode, conf)
		if result.Professional.YearsOfExperience != nil {
			payload.YearsOfExperience = &NumericFieldWithConfidence{
				Value:      *result.Professional.YearsOfExperience,
				Confidence: clampConfidence(conf),
			}
		}
	}

	// Map work history (cap at 10, sorted by descending confidence)
	if len(result.WorkHistory) > 0 {
		conf := scores.WorkHistory
		employers := make([]PastEmployerWithConfidence, 0, len(result.WorkHistory))
		for _, entry := range result.WorkHistory {
			employer := mapWorkHistoryEntry(entry, conf)
			employers = append(employers, employer)
		}
		// Sort by descending confidence
		sort.SliceStable(employers, func(i, j int) bool {
			return employers[i].Confidence > employers[j].Confidence
		})
		// Cap at maxCollectionItems
		if len(employers) > maxCollectionItems {
			employers = employers[:maxCollectionItems]
		}
		payload.PastEmployers = employers
	}

	// Map languages (cap at 10, sorted by descending confidence)
	if len(result.Languages) > 0 {
		conf := scores.Languages
		languages := make([]LanguageProficiencyWithConfidence, 0, len(result.Languages))
		for _, entry := range result.Languages {
			lang := mapLanguageEntry(entry, conf)
			languages = append(languages, lang)
		}
		// Sort by descending confidence
		sort.SliceStable(languages, func(i, j int) bool {
			return languages[i].Confidence > languages[j].Confidence
		})
		// Cap at maxCollectionItems
		if len(languages) > maxCollectionItems {
			languages = languages[:maxCollectionItems]
		}
		payload.LanguageProficiencies = languages
	}

	// Map references (cap at 10, sorted by descending confidence)
	if len(result.References) > 0 {
		conf := scores.References
		refs := make([]ReferenceWithConfidence, 0, len(result.References))
		for _, entry := range result.References {
			ref := mapReferenceEntry(entry, conf)
			refs = append(refs, ref)
		}
		// Sort by descending confidence
		sort.SliceStable(refs, func(i, j int) bool {
			return refs[i].Confidence > refs[j].Confidence
		})
		// Cap at maxCollectionItems
		if len(refs) > maxCollectionItems {
			refs = refs[:maxCollectionItems]
		}
		payload.References = refs
	}

	return payload
}

// getConfidenceScores returns the confidence scores from the result, or
// a zero-valued struct if nil.
func getConfidenceScores(scores *ConfidenceScores) ConfidenceScores {
	if scores == nil {
		return ConfidenceScores{}
	}
	return *scores
}

// unknownPlaceholders are literal strings a model sometimes returns instead
// of JSON null for a missing field, despite the schema and prompt asking for
// null. Claude on Bedrock has been observed returning "<UNKNOWN>" for CV
// fields it couldn't find; treating these as empty keeps a missing field
// from being autofilled as if it were real extracted data.
var unknownPlaceholders = map[string]struct{}{
	"<unknown>":    {},
	"unknown":      {},
	"n/a":          {},
	"not provided": {},
	"not found":    {},
	"none":         {},
}

// isUnknownPlaceholder reports whether value is a placeholder a model used
// in place of null, case-insensitively and ignoring surrounding whitespace.
func isUnknownPlaceholder(value string) bool {
	_, ok := unknownPlaceholders[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

// mapStringField creates a FieldWithConfidence from a string pointer.
// Returns nil if the pointer is nil, the string is empty, or the string is a
// placeholder the model used in place of null (see unknownPlaceholders).
func mapStringField(value *string, confidence float64) *FieldWithConfidence {
	if value == nil || *value == "" || isUnknownPlaceholder(*value) {
		return nil
	}
	return &FieldWithConfidence{
		Value:      *value,
		Confidence: clampConfidence(confidence),
	}
}

// mapWorkHistoryEntry converts a WorkHistoryEntry to a PastEmployerWithConfidence.
func mapWorkHistoryEntry(entry WorkHistoryEntry, sectionConfidence float64) PastEmployerWithConfidence {
	conf := clampConfidence(sectionConfidence)
	return PastEmployerWithConfidence{
		Name:        fieldFromPointer(entry.EmployerName, conf),
		PhoneNumber: fieldFromPointer(entry.PhoneNumber, conf),
		Location:    fieldFromPointer(entry.Location, conf),
		Confidence:  conf,
	}
}

// mapLanguageEntry converts a LanguageEntry to a LanguageProficiencyWithConfidence.
func mapLanguageEntry(entry LanguageEntry, sectionConfidence float64) LanguageProficiencyWithConfidence {
	conf := clampConfidence(sectionConfidence)
	return LanguageProficiencyWithConfidence{
		Language:         fieldFromPointer(entry.Language, conf),
		ProficiencyLevel: fieldFromPointer(entry.ProficiencyLevel, conf),
		WrittenSkills:    fieldFromPointer(entry.WrittenSkills, conf),
		Confidence:       conf,
	}
}

// mapReferenceEntry converts a ReferenceEntry to a ReferenceWithConfidence.
func mapReferenceEntry(entry ReferenceEntry, sectionConfidence float64) ReferenceWithConfidence {
	conf := clampConfidence(sectionConfidence)
	return ReferenceWithConfidence{
		Name:        fieldFromPointer(entry.Name, conf),
		PhoneNumber: fieldFromPointer(entry.PhoneNumber, conf),
		JobTitle:    fieldFromPointer(entry.JobTitle, conf),
		Email:       fieldFromPointer(entry.Email, conf),
		Location:    fieldFromPointer(entry.Location, conf),
		Confidence:  conf,
	}
}

// fieldFromPointer creates a FieldWithConfidence from a string pointer.
// Returns nil if the pointer is nil, the string is empty, or the string is a
// model placeholder for null (see unknownPlaceholders), matching
// mapStringField's behavior so collection fields omit unknown data instead
// of including empty-string or "<UNKNOWN>"-style placeholders.
func fieldFromPointer(value *string, confidence float64) *FieldWithConfidence {
	if value == nil || *value == "" || isUnknownPlaceholder(*value) {
		return nil
	}
	return &FieldWithConfidence{
		Value:      *value,
		Confidence: confidence,
	}
}

// clampConfidence restricts a confidence score to the range [0.0, 1.0]
// and rounds to 2 decimal places.
func clampConfidence(score float64) float64 {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return math.Round(score*100) / 100
}
