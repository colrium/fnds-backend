// Package counties provides the canonical list of Kenya's 47 counties and
// normalization utilities for matching free-text values to valid county names.
package counties

import (
	"errors"
	"strings"
)

// ErrInvalidCounty is returned when a non-empty county cannot be matched to
// one of Kenya's 47 official counties.
var ErrInvalidCounty = errors.New("county must be one of Kenya's 47 official counties")

// All contains the 47 Kenya counties in their official order (county codes
// 001-047). This is the single source of truth for valid county values on the
// backend.
var All = []string{
	"Mombasa",
	"Kwale",
	"Kilifi",
	"Tana River",
	"Lamu",
	"Taita-Taveta",
	"Garissa",
	"Wajir",
	"Mandera",
	"Marsabit",
	"Isiolo",
	"Meru",
	"Tharaka-Nithi",
	"Embu",
	"Kitui",
	"Machakos",
	"Makueni",
	"Nyandarua",
	"Nyeri",
	"Kirinyaga",
	"Murang'a",
	"Kiambu",
	"Turkana",
	"West Pokot",
	"Samburu",
	"Trans-Nzoia",
	"Uasin Gishu",
	"Elgeyo-Marakwet",
	"Nandi",
	"Baringo",
	"Laikipia",
	"Nakuru",
	"Narok",
	"Kajiado",
	"Kericho",
	"Bomet",
	"Kakamega",
	"Vihiga",
	"Bungoma",
	"Busia",
	"Siaya",
	"Kisumu",
	"Homa Bay",
	"Migori",
	"Kisii",
	"Nyamira",
	"Nairobi",
}

// lookup maps lowercase county names (and common variants) to the canonical name.
var lookup map[string]string

func init() {
	lookup = make(map[string]string, len(All)*3)
	for _, name := range All {
		lower := strings.ToLower(name)
		lookup[lower] = name
		// Also register with " county" suffix stripped/added for flexibility.
		lookup[lower+" county"] = name
	}
	// Common alternative spellings and aliases.
	lookup["muranga"] = "Murang'a"
	lookup["murang'a county"] = "Murang'a"
	lookup["muranga county"] = "Murang'a"
	lookup["taita taveta"] = "Taita-Taveta"
	lookup["taita taveta county"] = "Taita-Taveta"
	lookup["trans nzoia"] = "Trans-Nzoia"
	lookup["trans nzoia county"] = "Trans-Nzoia"
	lookup["elgeyo marakwet"] = "Elgeyo-Marakwet"
	lookup["elgeyo marakwet county"] = "Elgeyo-Marakwet"
	lookup["tharaka nithi"] = "Tharaka-Nithi"
	lookup["tharaka nithi county"] = "Tharaka-Nithi"
	lookup["west pokot county"] = "West Pokot"
	lookup["uasin gishu county"] = "Uasin Gishu"
	lookup["homa bay county"] = "Homa Bay"
	lookup["tana river county"] = "Tana River"
	lookup["nairobi city"] = "Nairobi"
	lookup["nairobi city county"] = "Nairobi"
}

// Normalize attempts to match a free-text county string to one of the 47
// canonical county names. It performs case-insensitive matching, strips common
// suffixes like "County", and checks known alternative spellings.
//
// Returns the canonical name and true if a match is found, or ("", false) if
// the input cannot be mapped to a valid county.
func Normalize(raw string) (string, bool) {
	cleaned := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if cleaned == "" {
		return "", false
	}

	canonical, ok := lookup[cleaned]
	return canonical, ok
}
