package profiles

import (
	"testing"
)

func TestBuildProfileUpdates_MapsAllScalarFields(t *testing.T) {
	yes := true
	req := ProfileUpdateRequest{
		IDNumber: "12345678", DateOfBirth: "1990-01-01", Gender: "male",
		HasSmartphone: &yes, IsBusinessRegistered: &yes, BusinessName: "Acme",
		BusinessRegistrationNumber: "R1", RecentGoodConduct: &yes,
		SocialMedias: `{"facebook":"me"}`, Address: "Nairobi",
		HighestEducationLevel: "College", Certificate: "BSc",
		SkillAcquisitionMode: "Self taught", Institution: "UoN",
		ProfessionalMembership: &yes, ProfessionalBody: "NCA",
		NextOfKinName: "Kin", NextOfKinPhoneNumber: "+254712345678", NextOfKinRelationship: "Wife",
		EarningGoalAmount: 50000,
		Skills:    []string{"s1", "s2"},
		Locations: []string{"loc1", "loc2"},
	}
	u := buildProfileUpdates(req)
	for _, k := range []string{
		"id_number", "date_of_birth", "gender", "has_smartphone", "is_business_registered",
		"business_name", "business_registration_number", "recent_good_conduct", "social_medias",
		"address", "highest_education_level", "certificate", "skill_acquisition_mode",
		"institution", "professional_membership", "professional_body", "next_of_kin_name",
		"next_of_kin_phone_number", "next_of_kin_relationship", "earning_goal_amount", "skills",
		"locations",
	} {
		if _, ok := u[k]; !ok {
			t.Fatalf("buildProfileUpdates missing key %q", k)
		}
	}
}
