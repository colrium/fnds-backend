package jobrequests

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestEmployerVacancyFilterScopesToOwner(t *testing.T) {
	t.Parallel()

	got := employerVacancyFilter("employer-1")
	want := bson.M{"owner_id": "employer-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("employerVacancyFilter() = %#v, want %#v", got, want)
	}
}

func TestPublicVacancyFilterOnlyReturnsOpenVacancies(t *testing.T) {
	t.Parallel()

	got := publicVacancyFilter()
	want := bson.M{"status": "open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("publicVacancyFilter() = %#v, want %#v", got, want)
	}
}
