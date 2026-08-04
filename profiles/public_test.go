package profiles

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"backend/configs"
	"backend/reviews"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestBuildPublicArtisanProfile_MapsAllPublicFields(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.example.test/")

	user := &users.User{
		ID:              "user-123",
		FundisID:        "FND-00123",
		FirstName:       "Alice",
		LastName:        "Kamau",
		Email:           "alice@example.test",
		Phone:           "+254700000000",
		Password:        []byte("secret"),
		FCMToken:        "private-push-token",
		Slug:            "alice-kamau-painter-nakuru",
		AvatarID:        "private-storage-key",
		IsAvailable:     true,
		IsVerified:      true,
		IsProfilePublic: true,
		UpdatedAt:       time.Unix(1_700_000_000, 123).UTC(),
	}
	profile := &Profile{
		BusinessName:                   "Kamau Painters",
		City:                           "Nakuru",
		State:                          "Nakuru County",
		Country:                        "Kenya",
		HighestEducationLevel:          "Degree",
		Institution:                    "UoN",
		ProfessionalMembership:         true,
		ProfessionalBody:               "Painters Association",
		SkillAcquisitionMode:           "formal_training",
		YearsOfExperience:              8,
		IDNumber:                       "12345678",
		DateOfBirth:                    "1990-01-01",
		Gender:                         "female",
		SocialMedias:                   "@alice",
		BusinessRegistrationNumber:     "BRN123",
		Address:                        "123 Main St",
		Zip:                            "00100",
		NextOfKinName:                  "Bob Kamau",
		NextOfKinPhoneNumber:           "+254711111111",
		CharacterReferenceName:         "Jane Doe",
		CharacterReferenceContact:      "+254722222222",
		CharacterReferenceRelationship: "Colleague",
		EarningGoalAmount:              50_000,
	}
	skills := []string{"Painting", "Decorating"}
	locations := []string{"Nakuru", "Naivasha"}
	artisanReviews := []reviews.Review{{Rating: 5}, {Rating: 4}}
	workingHours := []WorkingHourPublic{
		{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"},
	}
	languages := []LanguagePublic{
		{Language: "English", ProficiencyLevel: "fluent"},
	}
	pastEmployers := []PastEmployerPublic{
		{Name: "Grid Works", Location: "Nairobi"},
	}

	got := BuildPublicArtisanProfile(
		user,
		profile,
		skills,
		locations,
		artisanReviews,
		workingHours,
		languages,
		pastEmployers,
		12,
	)

	want := &PublicArtisanProfile{
		FundisID:               "FND-00123",
		Slug:                   "alice-kamau-painter-nakuru",
		FirstName:              "Alice",
		LastName:               "Kamau",
		AvatarURL:              "https://api.example.test/api/v1/avatars/user-123?v=1700000000000000123",
		BusinessName:           "Kamau Painters",
		City:                   "Nakuru",
		County:                 "Nakuru County",
		Country:                "Kenya",
		HighestEducationLevel:  "Degree",
		Institution:            "UoN",
		ProfessionalMembership: true,
		ProfessionalBody:       "Painters Association",
		SkillAcquisitionMode:   "formal_training",
		YearsOfExperience:      8,
		Skills:                 skills,
		Locations:              locations,
		IsAvailable:            true,
		IsVerified:             true,
		Rating:                 4.5,
		ReviewCount:            2,
		CompletedJobsCount:     12,
		WorkingHours:           workingHours,
		Languages:              languages,
		PastEmployers:          pastEmployers,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildPublicArtisanProfile() mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildPublicArtisanProfile_NormalizesNilInputs(t *testing.T) {
	got := BuildPublicArtisanProfile(
		&users.User{ID: "user-123", FirstName: "Alice", LastName: "Kamau"},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		0,
	)

	if got.Skills == nil {
		t.Error("Skills = nil, want an empty slice")
	}
	if got.Locations == nil {
		t.Error("Locations = nil, want an empty slice")
	}
	if got.WorkingHours == nil {
		t.Error("WorkingHours = nil, want an empty slice")
	}
	if got.Languages == nil {
		t.Error("Languages = nil, want an empty slice")
	}
	if got.PastEmployers == nil {
		t.Error("PastEmployers = nil, want an empty slice")
	}
	if got.Rating != 0 || got.ReviewCount != 0 {
		t.Errorf("rating summary = (%v, %d), want (0, 0)", got.Rating, got.ReviewCount)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"skills", "locations", "working_hours", "languages", "past_employers"} {
		value, ok := payload[key].([]any)
		if !ok || len(value) != 0 {
			t.Errorf("JSON field %q = %#v, want []", key, payload[key])
		}
	}
}

func TestBuildPublicArtisanProfile_JSONUsesStrictAllowList(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.example.test")
	got := BuildPublicArtisanProfile(
		&users.User{
			ID:          "user-123",
			FundisID:    "FND-00123",
			FirstName:   "Alice",
			LastName:    "Kamau",
			Email:       "alice@example.test",
			Phone:       "+254700000000",
			Password:    []byte("secret"),
			FCMToken:    "private-push-token",
			AvatarID:    "private-storage-key",
			Slug:        "alice-kamau-painter-nakuru",
			IsAvailable: true,
			IsVerified:  true,
			UpdatedAt:   time.Unix(1_700_000_000, 123).UTC(),
		},
		&Profile{
			BusinessName:                   "Kamau Painters",
			City:                           "Nakuru",
			State:                          "Nakuru County",
			Country:                        "Kenya",
			HighestEducationLevel:          "Degree",
			Institution:                    "UoN",
			ProfessionalMembership:         true,
			ProfessionalBody:               "Painters Association",
			SkillAcquisitionMode:           "formal_training",
			YearsOfExperience:              8,
			IDNumber:                       "12345678",
			DateOfBirth:                    "1990-01-01",
			Gender:                         "female",
			SocialMedias:                   "@alice",
			BusinessRegistrationNumber:     "BRN123",
			Address:                        "123 Main St",
			Zip:                            "00100",
			NextOfKinName:                  "Bob Kamau",
			NextOfKinPhoneNumber:           "+254711111111",
			NextOfKinRelationship:          "Sibling",
			CharacterReferenceName:         "Jane Doe",
			CharacterReferenceContact:      "+254722222222",
			CharacterReferenceRelationship: "Colleague",
			EarningGoalAmount:              50_000,
		},
		[]string{"Painting"},
		[]string{"Nakuru"},
		[]reviews.Review{{Rating: 5}},
		[]WorkingHourPublic{{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"}},
		[]LanguagePublic{{Language: "English", ProficiencyLevel: "fluent"}},
		[]PastEmployerPublic{{Name: "Grid Works", Location: "Nairobi"}},
		12,
	)

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	wantKeys := []string{
		"avatar_url", "business_name", "city", "completed_jobs_count", "country",
		"county", "first_name", "fundis_id", "highest_education_level", "institution",
		"is_available", "is_verified", "languages", "last_name", "locations",
		"past_employers", "professional_body", "professional_membership", "rating",
		"review_count", "skill_acquisition_mode", "skills", "slug",
		"working_hours", "years_of_experience",
	}
	gotKeys := make([]string, 0, len(payload))
	for key := range payload {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("public JSON keys = %v, want strict allow-list %v", gotKeys, wantKeys)
	}

	assertNestedJSONKeys(t, payload, "working_hours", []string{"closing_time", "day", "opening_time"})
	assertNestedJSONKeys(t, payload, "languages", []string{"language", "proficiency_level"})
	assertNestedJSONKeys(t, payload, "past_employers", []string{"location", "name"})
}

func TestGetPublicWorkingHours(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("maps records in database order", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".working_hours",
			mtest.FirstBatch,
			bson.D{{Key: "user_id", Value: "user-123"}, {Key: "day", Value: "Monday"}, {Key: "opening_time", Value: "08:00"}, {Key: "closing_time", Value: "17:00"}},
			bson.D{{Key: "user_id", Value: "user-123"}, {Key: "day", Value: "Tuesday"}, {Key: "opening_time", Value: "09:00"}, {Key: "closing_time", Value: "16:00"}},
		))

		got, err := getPublicWorkingHours(context.Background(), "user-123")
		if err != nil {
			mt.Fatalf("getPublicWorkingHours() error = %v", err)
		}
		want := []WorkingHourPublic{
			{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"},
			{Day: "Tuesday", OpeningTime: "09:00", ClosingTime: "16:00"},
		}
		if !reflect.DeepEqual(got, want) {
			mt.Errorf("getPublicWorkingHours() = %#v, want %#v", got, want)
		}
		assertFindCommand(mt, "working_hours", "user-123")
	})
	mt.Run("returns cursor decode errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".working_hours",
			mtest.FirstBatch,
			bson.D{{Key: "day", Value: int32(1)}},
		))

		got, err := getPublicWorkingHours(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicWorkingHours() error = nil, want cursor decode error")
		}
		if !strings.Contains(err.Error(), "cannot decode") {
			mt.Errorf("getPublicWorkingHours() error = %v, want cursor decode error", err)
		}
		if got != nil {
			mt.Errorf("getPublicWorkingHours() = %#v, want nil after cursor decode error", got)
		}
	})
	mt.Run("returns database errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "find failed"}))

		got, err := getPublicWorkingHours(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicWorkingHours() error = nil, want database error")
		}
		if got != nil {
			mt.Errorf("getPublicWorkingHours() = %#v, want nil after database error", got)
		}
	})
}

func TestGetPublicLanguages(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("maps only public language fields", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".language_proficiencies",
			mtest.FirstBatch,
			bson.D{{Key: "user_id", Value: "user-123"}, {Key: "language", Value: "English"}, {Key: "proficiency_level", Value: "fluent"}, {Key: "written_skills", Value: "private detail"}},
		))

		got, err := getPublicLanguages(context.Background(), "user-123")
		if err != nil {
			mt.Fatalf("getPublicLanguages() error = %v", err)
		}
		want := []LanguagePublic{{Language: "English", ProficiencyLevel: "fluent"}}
		if !reflect.DeepEqual(got, want) {
			mt.Errorf("getPublicLanguages() = %#v, want %#v", got, want)
		}
		assertFindCommand(mt, "language_proficiencies", "user-123")
	})
	mt.Run("returns cursor decode errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".language_proficiencies",
			mtest.FirstBatch,
			bson.D{{Key: "language", Value: int32(1)}},
		))

		got, err := getPublicLanguages(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicLanguages() error = nil, want cursor decode error")
		}
		if !strings.Contains(err.Error(), "cannot decode") {
			mt.Errorf("getPublicLanguages() error = %v, want cursor decode error", err)
		}
		if got != nil {
			mt.Errorf("getPublicLanguages() = %#v, want nil after cursor decode error", got)
		}
	})
	mt.Run("returns database errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "find failed"}))

		got, err := getPublicLanguages(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicLanguages() error = nil, want database error")
		}
		if got != nil {
			mt.Errorf("getPublicLanguages() = %#v, want nil after database error", got)
		}
	})
}

func TestGetPublicPastEmployers(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("drops PII and records without a name", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".past_employers",
			mtest.FirstBatch,
			bson.D{{Key: "user_id", Value: "user-123"}, {Key: "name", Value: "Grid Works"}, {Key: "location", Value: "Nairobi"}, {Key: "phone_number", Value: "+254700000000"}},
			bson.D{{Key: "user_id", Value: "user-123"}, {Key: "name", Value: ""}, {Key: "location", Value: "Nakuru"}, {Key: "phone_number", Value: "+254711111111"}},
		))

		got, err := getPublicPastEmployers(context.Background(), "user-123")
		if err != nil {
			mt.Fatalf("getPublicPastEmployers() error = %v", err)
		}
		want := []PastEmployerPublic{{Name: "Grid Works", Location: "Nairobi"}}
		if !reflect.DeepEqual(got, want) {
			mt.Errorf("getPublicPastEmployers() = %#v, want %#v", got, want)
		}
		assertFindCommand(mt, "past_employers", "user-123")
	})
	mt.Run("returns cursor decode errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".past_employers",
			mtest.FirstBatch,
			bson.D{{Key: "name", Value: int32(1)}},
		))

		got, err := getPublicPastEmployers(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicPastEmployers() error = nil, want cursor decode error")
		}
		if !strings.Contains(err.Error(), "cannot decode") {
			mt.Errorf("getPublicPastEmployers() error = %v, want cursor decode error", err)
		}
		if got != nil {
			mt.Errorf("getPublicPastEmployers() = %#v, want nil after cursor decode error", got)
		}
	})
	mt.Run("returns database errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "find failed"}))

		got, err := getPublicPastEmployers(context.Background(), "user-123")
		if err == nil {
			mt.Fatal("getPublicPastEmployers() error = nil, want database error")
		}
		if got != nil {
			mt.Errorf("getPublicPastEmployers() = %#v, want nil after database error", got)
		}
	})
}

func TestCountCompletedJobs(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("counts only completed jobs for the artisan", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".job_requests",
			mtest.FirstBatch,
			bson.D{{Key: "n", Value: int32(7)}},
		))

		got, err := countCompletedJobs(context.Background(), "artisan-123")
		if err != nil {
			mt.Fatalf("countCompletedJobs() error = %v", err)
		}
		if got != 7 {
			mt.Errorf("countCompletedJobs() = %d, want 7", got)
		}
		assertCompletedJobsCountCommand(mt, "artisan-123")
	})
	mt.Run("returns database errors", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 123, Message: "aggregate failed"}))

		got, err := countCompletedJobs(context.Background(), "artisan-123")
		if err == nil {
			mt.Fatal("countCompletedJobs() error = nil, want database error")
		}
		if got != 0 {
			mt.Errorf("countCompletedJobs() = %d, want 0 after database error", got)
		}
	})
}

func assertNestedJSONKeys(t *testing.T, payload map[string]any, field string, want []string) {
	t.Helper()
	items, ok := payload[field].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("JSON field %q = %#v, want one object", field, payload[field])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("JSON field %q item = %#v, want object", field, items[0])
	}
	got := make([]string, 0, len(item))
	for key := range item {
		got = append(got, key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSON field %q keys = %v, want %v", field, got, want)
	}
}

func useMockMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() {
		configs.MI = previous
	})
}

func assertFindCommand(mt *mtest.T, collection, userID string) {
	mt.Helper()
	event := mt.GetStartedEvent()
	if event == nil {
		mt.Fatal("MongoDB find command was not recorded")
	}
	if event.CommandName != "find" {
		mt.Fatalf("MongoDB command = %q, want find", event.CommandName)
	}
	if got := event.Command.Lookup("find").StringValue(); got != collection {
		mt.Errorf("find collection = %q, want %q", got, collection)
	}
	var filter bson.M
	if err := bson.Unmarshal(event.Command.Lookup("filter").Document(), &filter); err != nil {
		mt.Fatalf("decode find filter: %v", err)
	}
	if !reflect.DeepEqual(filter, bson.M{"user_id": userID}) {
		mt.Errorf("find filter = %#v, want user_id %q", filter, userID)
	}
	var sortDocument bson.M
	if err := bson.Unmarshal(event.Command.Lookup("sort").Document(), &sortDocument); err != nil {
		mt.Fatalf("decode find sort: %v", err)
	}
	if !reflect.DeepEqual(sortDocument, bson.M{"created_at": int32(1)}) {
		mt.Errorf("find sort = %#v, want created_at ascending", sortDocument)
	}
}

func assertCompletedJobsCountCommand(mt *mtest.T, artisanID string) {
	mt.Helper()
	event := mt.GetStartedEvent()
	if event == nil {
		mt.Fatal("MongoDB count command was not recorded")
	}
	if event.CommandName != "aggregate" {
		mt.Fatalf("MongoDB command = %q, want aggregate", event.CommandName)
	}
	if got := event.Command.Lookup("aggregate").StringValue(); got != "job_requests" {
		mt.Errorf("aggregate collection = %q, want job_requests", got)
	}
	stages, err := event.Command.Lookup("pipeline").Array().Values()
	if err != nil {
		mt.Fatalf("decode count pipeline: %v", err)
	}
	if len(stages) == 0 {
		mt.Fatal("count pipeline is empty")
	}
	var match bson.M
	if err := bson.Unmarshal(stages[0].Document().Lookup("$match").Document(), &match); err != nil {
		mt.Fatalf("decode count match stage: %v", err)
	}
	want := bson.M{"artisan_id": artisanID, "status": "completed"}
	if !reflect.DeepEqual(match, want) {
		mt.Errorf("count match = %#v, want %#v", match, want)
	}
}
