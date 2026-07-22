package profiles

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestSkillMatchFilter(t *testing.T) {
	got := skillMatchFilter("skill-electrician", "Electrician")
	want := bson.M{
		"$or": bson.A{
			bson.M{"skills": "skill-electrician"},
			bson.M{"skills": bson.M{"$regex": "^Electrician$", "$options": "i"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillMatchFilter() = %#v, want %#v", got, want)
	}
}
