package profiles

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestBuildProfileUpdates_MapsAllScalarFields(t *testing.T) {
	yes := true
	req := ProfileUpdateRequest{
		IDNumber: "12345678", DateOfBirth: "1990-01-01", Gender: "male",
		HasSmartphone: &yes, IsBusinessRegistered: &yes, BusinessName: "Acme",
		BusinessRegistrationNumber: "R1", RecentGoodConduct: &yes,
		SocialMedias: `{"facebook":"me"}`, Address: "Nairobi", City: "Nairobi",
		County: "Nairobi", Country: "Kenya", Zip: "00100",
		HighestEducationLevel: "College", Certificate: "BSc",
		SkillAcquisitionMode: "Self taught", Institution: "UoN",
		ProfessionalMembership: &yes, ProfessionalBody: "NCA",
		NextOfKinName: "Kin", NextOfKinPhoneNumber: "+254712345678", NextOfKinRelationship: "Wife",
		EarningGoalAmount: 50000,
		Skills:            []string{"s1", "s2"},
		Locations:         []string{"loc1", "loc2"},
	}
	u := buildProfileUpdates(req)
	for _, k := range []string{
		"id_number", "date_of_birth", "gender", "has_smartphone", "is_business_registered",
		"business_name", "business_registration_number", "recent_good_conduct", "social_medias",
		"address", "city", "county", "zip", "country", "highest_education_level", "certificate", "skill_acquisition_mode",
		"institution", "professional_membership", "professional_body", "next_of_kin_name",
		"next_of_kin_phone_number", "next_of_kin_relationship", "earning_goal_amount", "skills",
		"locations",
	} {
		if _, ok := u[k]; !ok {
			t.Fatalf("buildProfileUpdates missing key %q", k)
		}
	}
}

func TestValidateProfileUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		skills  []string
		wantErr bool
	}{
		{name: "omitted skill is valid for draft", skills: nil},
		{name: "empty skill list is valid for draft", skills: []string{}},
		{name: "one skill is valid", skills: []string{"skill-1"}},
		{name: "blank skill is invalid", skills: []string{" "}, wantErr: true},
		{
			name:    "multiple skills are invalid",
			skills:  []string{"skill-1", "skill-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateProfileUpdate(ProfileUpdateRequest{Skills: tt.skills})
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateProfileUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateProfileCountyValidation(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("rejects invalid county before database lookup", func(mt *mtest.T) {
		useMockMongo(mt)

		err := UpdateProfile(context.Background(), "user-1", ProfileUpdateRequest{County: "California"})
		if !errors.Is(err, ErrInvalidCounty) {
			mt.Fatalf("UpdateProfile() error = %v, want ErrInvalidCounty", err)
		}
		if events := mt.GetAllStartedEvents(); len(events) != 0 {
			mt.Fatalf("invalid county triggered %d MongoDB command(s), want 0", len(events))
		}
	})

	mt.Run("persists canonical county", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(
			userCursorResponse(mt, bson.D{{Key: "_id", Value: "user-1"}}),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		err := UpdateProfile(context.Background(), "user-1", ProfileUpdateRequest{County: "Nairobi  County"})
		if err != nil {
			mt.Fatalf("UpdateProfile() error = %v", err)
		}

		for _, event := range mt.GetAllStartedEvents() {
			if event.CommandName != "update" || event.Command.Lookup("update").StringValue() != "profiles" {
				continue
			}
			updates, err := event.Command.Lookup("updates").Array().Values()
			if err != nil || len(updates) != 1 {
				mt.Fatalf("decode profile updates: values=%v error=%v", updates, err)
			}
			got := updates[0].Document().Lookup("u").Document().Lookup("$set").Document().Lookup("county").StringValue()
			if got != "Nairobi" {
				mt.Fatalf("persisted county = %q, want Nairobi", got)
			}
			return
		}
		mt.Fatal("profile update command was not recorded")
	})
}
