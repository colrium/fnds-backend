package profiles

import "strings"

type ProfileCompletion struct {
	Complete        bool     `json:"complete"`
	Percent         int      `json:"percent"`
	MissingSections []string `json:"missing_sections"`
}

func GetProfileCompletion(profile *FullProfile) ProfileCompletion {
	if profile == nil {
		return ProfileCompletion{
			MissingSections: profileSectionNames(),
		}
	}

	sections := []struct {
		name     string
		complete bool
	}{
		{
			name: "Personal details",
			complete: hasText(profile.IDNumber) &&
				hasText(profile.DateOfBirth) &&
				hasText(profile.Gender) &&
				hasText(profile.Address),
		},
		{
			name: "Skills and experience",
			complete: len(profile.Skills) > 0 &&
				hasText(profile.SkillAcquisitionMode),
		},
		{
			name:     "References",
			complete: hasCompleteReference(profile.References),
		},
		{
			name: "Next of kin",
			complete: hasText(profile.NextOfKinName) &&
				hasText(profile.NextOfKinPhoneNumber) &&
				hasText(profile.NextOfKinRelationship),
		},
		{
			name:     "Service locations",
			complete: len(profile.Locations) > 0,
		},
		{
			name:     "Working hours",
			complete: len(profile.WorkingHours) > 0,
		},
	}

	missing := make([]string, 0, len(sections))
	for _, section := range sections {
		if !section.complete {
			missing = append(missing, section.name)
		}
	}

	completed := len(sections) - len(missing)
	return ProfileCompletion{
		Complete:        completed == len(sections),
		Percent:         (completed*100 + len(sections)/2) / len(sections),
		MissingSections: missing,
	}
}

func hasCompleteReference(references []ReferenceInput) bool {
	for _, reference := range references {
		if hasText(reference.Name) && hasText(reference.PhoneNumber) {
			return true
		}
	}
	return false
}

func hasText(value string) bool {
	return strings.TrimSpace(value) != ""
}

func profileSectionNames() []string {
	return []string{
		"Personal details",
		"Skills and experience",
		"References",
		"Next of kin",
		"Service locations",
		"Working hours",
	}
}
