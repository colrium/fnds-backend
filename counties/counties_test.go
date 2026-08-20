package counties

import "testing"

func TestAll_Has47Counties(t *testing.T) {
	t.Parallel()
	if len(All) != 47 {
		t.Fatalf("All has %d counties, want 47", len(All))
	}
}

func TestNormalize_ExactMatch(t *testing.T) {
	t.Parallel()
	for _, county := range All {
		got, ok := Normalize(county)
		if !ok {
			t.Errorf("Normalize(%q) returned not-found", county)
			continue
		}
		if got != county {
			t.Errorf("Normalize(%q) = %q, want %q", county, got, county)
		}
	}
}

func TestNormalize_CaseInsensitive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"nairobi", "Nairobi"},
		{"NAIROBI", "Nairobi"},
		{"Nairobi", "Nairobi"},
		{"mombasa", "Mombasa"},
		{"UASIN GISHU", "Uasin Gishu"},
	}
	for _, tt := range tests {
		got, ok := Normalize(tt.input)
		if !ok {
			t.Errorf("Normalize(%q) returned not-found", tt.input)
			continue
		}
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalize_CountySuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"Nairobi County", "Nairobi"},
		{"nairobi county", "Nairobi"},
		{"  Nairobi  County  ", "Nairobi"},
		{"Mombasa County", "Mombasa"},
		{"Uasin\tGishu\nCounty", "Uasin Gishu"},
	}
	for _, tt := range tests {
		got, ok := Normalize(tt.input)
		if !ok {
			t.Errorf("Normalize(%q) returned not-found", tt.input)
			continue
		}
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalize_Aliases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"Muranga", "Murang'a"},
		{"Nairobi City", "Nairobi"},
		{"Nairobi City County", "Nairobi"},
		{"Taita Taveta", "Taita-Taveta"},
		{"Trans Nzoia", "Trans-Nzoia"},
		{"Elgeyo Marakwet", "Elgeyo-Marakwet"},
		{"Tharaka Nithi", "Tharaka-Nithi"},
	}
	for _, tt := range tests {
		got, ok := Normalize(tt.input)
		if !ok {
			t.Errorf("Normalize(%q) returned not-found", tt.input)
			continue
		}
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalize_EmptyAndInvalid(t *testing.T) {
	t.Parallel()
	invalids := []string{"", "  ", "InvalidCounty", "California", "Lagos"}
	for _, input := range invalids {
		_, ok := Normalize(input)
		if ok {
			t.Errorf("Normalize(%q) should return not-found", input)
		}
	}
}
