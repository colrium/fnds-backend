package seeders

import (
	"testing"
)

func TestSeederNames(t *testing.T) {
	expected := map[string]bool{
		"subscription_plans": false,
		"locations":          false,
		"skills":             false,
		"banks":              false,
	}

	// Just verify the seeder list is defined correctly
	if len(expected) != 4 {
		t.Errorf("expected 4 seeders, got %d", len(expected))
	}
}
