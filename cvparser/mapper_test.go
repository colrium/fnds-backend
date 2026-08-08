package cvparser

import (
	"testing"
)

func ptr(s string) *string { return &s }
func intPtr(i int) *int    { return &i }

func TestMapToAutofill_NilResult(t *testing.T) {
	t.Parallel()
	m := NewMapper()
	payload := m.MapToAutofill(nil)
	if payload == nil {
		t.Fatal("MapToAutofill(nil) returned nil, want empty payload")
	}
	if countPopulatedFields(payload) != 0 {
		t.Fatalf("MapToAutofill(nil) field count = %d, want 0", countPopulatedFields(payload))
	}
}

func TestMapToAutofill_NilFieldsOmitted(t *testing.T) {
	t.Parallel()
	m := NewMapper()
	result := &ExtractionResult{
		PersonalDetails: &PersonalDetails{
			Address: nil,
			City:    ptr("Nairobi"),
			State:   nil,
		},
		ConfidenceScores: &ConfidenceScores{PersonalDetails: 0.9},
	}
	payload := m.MapToAutofill(result)
	if payload.Address != nil {
		t.Fatal("expected nil Address for nil input")
	}
	if payload.City == nil || payload.City.Value != "Nairobi" {
		t.Fatalf("City = %v, want Nairobi", payload.City)
	}
	if payload.State != nil {
		t.Fatal("expected nil State for nil input")
	}
}

func TestMapToAutofill_EmptyStringsOmitted(t *testing.T) {
	t.Parallel()
	m := NewMapper()
	result := &ExtractionResult{
		PersonalDetails: &PersonalDetails{
			Address: ptr(""),
			City:    ptr("Mombasa"),
		},
		ConfidenceScores: &ConfidenceScores{PersonalDetails: 0.8},
	}
	payload := m.MapToAutofill(result)
	if payload.Address != nil {
		t.Fatal("expected nil Address for empty string input")
	}
	if payload.City == nil || payload.City.Value != "Mombasa" {
		t.Fatalf("City = %v, want Mombasa", payload.City)
	}
}

func TestMapToAutofill_UnknownPlaceholdersFiltered(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	placeholders := []string{"<UNKNOWN>", "N/A", "not provided", "None", " Unknown "}
	for _, placeholder := range placeholders {
		result := &ExtractionResult{
			PersonalDetails:  &PersonalDetails{Address: ptr(placeholder)},
			ConfidenceScores: &ConfidenceScores{PersonalDetails: 0.9},
		}
		payload := m.MapToAutofill(result)
		if payload.Address != nil {
			t.Fatalf("placeholder %q was not filtered, got %v", placeholder, payload.Address)
		}
	}
}

func TestMapToAutofill_ConfidenceClamping(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	tests := []struct {
		name  string
		score float64
		want  float64
	}{
		{"negative clamped to 0", -0.5, 0},
		{"above 1 clamped to 1", 1.5, 1},
		{"normal rounded", 0.856, 0.86},
		{"zero stays zero", 0, 0},
		{"one stays one", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := &ExtractionResult{
				PersonalDetails:  &PersonalDetails{City: ptr("Nairobi")},
				ConfidenceScores: &ConfidenceScores{PersonalDetails: tt.score},
			}
			payload := m.MapToAutofill(result)
			if payload.City == nil {
				t.Fatal("expected non-nil City")
			}
			if payload.City.Confidence != tt.want {
				t.Fatalf("confidence = %v, want %v", payload.City.Confidence, tt.want)
			}
		})
	}
}

func TestMapToAutofill_CollectionCap(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	entries := make([]WorkHistoryEntry, 15)
	for i := range entries {
		name := "Employer"
		entries[i] = WorkHistoryEntry{EmployerName: &name}
	}

	result := &ExtractionResult{
		WorkHistory:      entries,
		ConfidenceScores: &ConfidenceScores{WorkHistory: 0.8},
	}
	payload := m.MapToAutofill(result)
	if len(payload.PastEmployers) != maxCollectionItems {
		t.Fatalf("PastEmployers count = %d, want %d", len(payload.PastEmployers), maxCollectionItems)
	}
}

func TestMapToAutofill_CollectionSortedByConfidence(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	result := &ExtractionResult{
		Languages: []LanguageEntry{
			{Language: ptr("English")},
			{Language: ptr("Swahili")},
		},
		ConfidenceScores: &ConfidenceScores{Languages: 0.7},
	}
	payload := m.MapToAutofill(result)
	if len(payload.LanguageProficiencies) != 2 {
		t.Fatalf("languages count = %d, want 2", len(payload.LanguageProficiencies))
	}
	for i := 1; i < len(payload.LanguageProficiencies); i++ {
		if payload.LanguageProficiencies[i].Confidence > payload.LanguageProficiencies[i-1].Confidence {
			t.Fatal("languages not sorted by descending confidence")
		}
	}
}

func TestMapToAutofill_YearsOfExperience(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	result := &ExtractionResult{
		Professional:     &Professional{YearsOfExperience: intPtr(5)},
		ConfidenceScores: &ConfidenceScores{Professional: 0.9},
	}
	payload := m.MapToAutofill(result)
	if payload.YearsOfExperience == nil {
		t.Fatal("expected non-nil YearsOfExperience")
	}
	if payload.YearsOfExperience.Value != 5 {
		t.Fatalf("YearsOfExperience = %d, want 5", payload.YearsOfExperience.Value)
	}
}

func TestMapToAutofill_NilYearsOfExperienceOmitted(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	result := &ExtractionResult{
		Professional:     &Professional{YearsOfExperience: nil},
		ConfidenceScores: &ConfidenceScores{Professional: 0.9},
	}
	payload := m.MapToAutofill(result)
	if payload.YearsOfExperience != nil {
		t.Fatal("expected nil YearsOfExperience for nil input")
	}
}

func TestMapToAutofill_NilConfidenceScoresDefault(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	result := &ExtractionResult{
		PersonalDetails:  &PersonalDetails{City: ptr("Nairobi")},
		ConfidenceScores: nil,
	}
	payload := m.MapToAutofill(result)
	if payload.City == nil {
		t.Fatal("expected non-nil City")
	}
	if payload.City.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0 (default)", payload.City.Confidence)
	}
}

func TestMapToAutofill_ReferencesMapAllFields(t *testing.T) {
	t.Parallel()
	m := NewMapper()

	result := &ExtractionResult{
		References: []ReferenceEntry{
			{
				Name:        ptr("John Doe"),
				PhoneNumber: ptr("+254700000000"),
				JobTitle:    ptr("Manager"),
				Email:       ptr("john@example.com"),
				Location:    ptr("Nairobi"),
			},
		},
		ConfidenceScores: &ConfidenceScores{References: 0.85},
	}
	payload := m.MapToAutofill(result)
	if len(payload.References) != 1 {
		t.Fatalf("references count = %d, want 1", len(payload.References))
	}
	ref := payload.References[0]
	if ref.Name == nil || ref.Name.Value != "John Doe" {
		t.Fatalf("ref.Name = %v, want John Doe", ref.Name)
	}
	if ref.PhoneNumber == nil || ref.PhoneNumber.Value != "+254700000000" {
		t.Fatalf("ref.PhoneNumber = %v", ref.PhoneNumber)
	}
	if ref.JobTitle == nil || ref.JobTitle.Value != "Manager" {
		t.Fatalf("ref.JobTitle = %v", ref.JobTitle)
	}
	if ref.Email == nil || ref.Email.Value != "john@example.com" {
		t.Fatalf("ref.Email = %v", ref.Email)
	}
	if ref.Location == nil || ref.Location.Value != "Nairobi" {
		t.Fatalf("ref.Location = %v", ref.Location)
	}
}
