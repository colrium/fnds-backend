package cvparser

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"backend/counties"
)

func TestCVExtractionSchemaCountyShape(t *testing.T) {
	t.Parallel()

	type schemaProperty struct {
		Type        json.RawMessage           `json:"type"`
		Description string                    `json:"description"`
		Properties  map[string]schemaProperty `json:"properties"`
		Required    []string                  `json:"required"`
	}
	var schema struct {
		Properties map[string]schemaProperty `json:"properties"`
	}
	if err := json.Unmarshal([]byte(cvExtractionInputSchema), &schema); err != nil {
		t.Fatalf("unmarshal cvExtractionInputSchema: %v", err)
	}

	personalDetails, ok := schema.Properties["personal_details"]
	if !ok {
		t.Fatal("schema is missing personal_details")
	}
	countyProperty, ok := personalDetails.Properties["county"]
	if !ok {
		t.Fatal("personal_details schema is missing county")
	}
	var countyTypes []string
	if err := json.Unmarshal(countyProperty.Type, &countyTypes); err != nil {
		t.Fatalf("unmarshal county type: %v", err)
	}
	if want := []string{"string", "null"}; !reflect.DeepEqual(countyTypes, want) {
		t.Fatalf("county type = %v, want %v", countyTypes, want)
	}
	countyDescription := strings.ToLower(countyProperty.Description)
	for _, countyName := range counties.All {
		if strings.Contains(countyDescription, strings.ToLower(countyName)) {
			t.Fatalf("county description duplicates canonical county name %q: %q", countyName, countyProperty.Description)
		}
	}
	if want := []string{"address", "city"}; !reflect.DeepEqual(personalDetails.Required, want) {
		t.Fatalf("personal_details required = %v, want %v", personalDetails.Required, want)
	}
}
