package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGenerateSlug(t *testing.T) {
	tests := []struct {
		name      string
		firstName string
		lastName  string
		skill     string
		city      string
		want      string
	}{
		{
			name:      "basic slug",
			firstName: "Dennis",
			lastName:  "Mwangi",
			skill:     "Plumber",
			city:      "Nairobi",
			want:      "dennis-mwangi-plumber-nairobi",
		},
		{
			name:      "multi-word skill and city",
			firstName: "Alice",
			lastName:  "Kamau",
			skill:     "Interior Designer",
			city:      "Thika Town",
			want:      "alice-kamau-interior-designer-thika-town",
		},
		{
			name:      "special characters removed",
			firstName: "John",
			lastName:  "O'Brien",
			skill:     "Electrician",
			city:      "Nairobi (CBD)",
			want:      "john-obrien-electrician-nairobi-cbd",
		},
		{
			name:      "extra whitespace normalized",
			firstName: "  Jane  ",
			lastName:  "  Doe  ",
			skill:     " Plumber ",
			city:      " Mombasa ",
			want:      "jane-doe-plumber-mombasa",
		},
		{
			name:      "empty skill and city",
			firstName: "Peter",
			lastName:  "Ochieng",
			skill:     "",
			city:      "",
			want:      "peter-ochieng",
		},
		{
			name:      "all empty",
			firstName: "",
			lastName:  "",
			skill:     "",
			city:      "",
			want:      "",
		},
		{
			name:      "unicode characters stripped",
			firstName: "Dennis",
			lastName:  "Mwangi",
			skill:     "Plumber!@#$%",
			city:      "Nairobi",
			want:      "dennis-mwangi-plumber-nairobi",
		},
		{
			name:      "numbers preserved",
			firstName: "Tech",
			lastName:  "Pro2024",
			skill:     "HVAC",
			city:      "Kisumu",
			want:      "tech-pro2024-hvac-kisumu",
		},
		{
			name:      "hyphens in names",
			firstName: "Anne-Marie",
			lastName:  "Njoki",
			skill:     "Mason",
			city:      "Eldoret",
			want:      "anne-marie-njoki-mason-eldoret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateSlug(tt.firstName, tt.lastName, tt.skill, tt.city)
			if got != tt.want {
				t.Errorf("GenerateSlug(%q, %q, %q, %q) = %q, want %q",
					tt.firstName, tt.lastName, tt.skill, tt.city, got, tt.want)
			}
		})
	}
}

func TestPublishWithUniqueSlug_EmptySlug(t *testing.T) {
	_, err := PublishWithUniqueSlug(context.Background(), "user-id", "")
	if err == nil {
		t.Error("PublishWithUniqueSlug with an empty slug should return an error")
	}
}

func TestPublishWithUniqueSlugValidation(t *testing.T) {
	tests := []struct {
		name   string
		ctx    context.Context
		userID string
		base   string
		want   string
		is     error
	}{
		{
			name: "empty user ID",
			ctx:  context.Background(),
			base: "artisan",
			want: "user ID cannot be empty",
		},
		{
			name:   "empty base",
			ctx:    context.Background(),
			userID: "user-id",
			want:   "cannot publish profile with empty slug",
		},
		{
			name: "pre-cancelled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			userID: "user-id",
			base:   "artisan",
			want:   "publishing profile",
			is:     context.Canceled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PublishWithUniqueSlug(test.ctx, test.userID, test.base)
			if got != "" {
				t.Errorf("PublishWithUniqueSlug() slug = %q, want empty", got)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PublishWithUniqueSlug() error = %v, want containing %q", err, test.want)
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Errorf("PublishWithUniqueSlug() error = %v, want wrapping %v", err, test.is)
			}
		})
	}
}

func TestPublishWithUniqueSlugMongoBranches(t *testing.T) {
	tests := []struct {
		name      string
		responses []bson.D
		wantSlug  string
		wantError string
		wantAlso  string
		wantIs    error
	}{
		{
			name: "success",
			responses: []bson.D{
				mtest.CreateSuccessResponse(
					bson.E{Key: "n", Value: 1},
					bson.E{Key: "nModified", Value: 1},
				),
			},
			wantSlug: "jane-doe-plumber",
		},
		{
			name: "duplicate retries with numeric suffix",
			responses: []bson.D{
				mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 11000, Message: "duplicate slug"}),
				mtest.CreateSuccessResponse(
					bson.E{Key: "n", Value: 1},
					bson.E{Key: "nModified", Value: 1},
				),
			},
			wantSlug: "jane-doe-plumber-2",
		},
		{
			name: "unmatched user wraps no documents",
			responses: []bson.D{
				mtest.CreateSuccessResponse(
					bson.E{Key: "n", Value: 0},
					bson.E{Key: "nModified", Value: 0},
				),
			},
			wantError: "user not found when publishing profile",
			wantIs:    mongo.ErrNoDocuments,
		},
		{
			name: "non-duplicate update error",
			responses: []bson.D{
				mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "bad value"}),
			},
			wantError: "publishing profile with slug \"jane-doe-plumber\"",
			wantAlso:  "bad value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mt := useMockMongo(t, "users")
			mt.AddMockResponses(test.responses...)

			got, err := PublishWithUniqueSlug(context.Background(), "user-id", "jane-doe-plumber")
			if got != test.wantSlug {
				t.Errorf("PublishWithUniqueSlug() slug = %q, want %q", got, test.wantSlug)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("PublishWithUniqueSlug() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("PublishWithUniqueSlug() error = %v, want containing %q", err, test.wantError)
			}
			if test.wantAlso != "" && !strings.Contains(err.Error(), test.wantAlso) {
				t.Errorf("PublishWithUniqueSlug() error = %v, want also containing %q", err, test.wantAlso)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Errorf("PublishWithUniqueSlug() error = %v, want wrapping %v", err, test.wantIs)
			}
		})
	}
}

func TestPublishWithUniqueSlugExhaustsRetries(t *testing.T) {
	mt := useMockMongo(t, "users")
	responses := make([]bson.D, 100)
	for index := range responses {
		responses[index] = mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    11000,
			Message: "duplicate slug",
		})
	}
	mt.AddMockResponses(responses...)

	got, err := PublishWithUniqueSlug(context.Background(), "user-id", "artisan")
	if got != "" {
		t.Errorf("PublishWithUniqueSlug() slug = %q, want empty", got)
	}
	if err == nil || err.Error() != "could not reserve unique slug after 100 attempts" {
		t.Errorf("PublishWithUniqueSlug() error = %v, want exhaustion error", err)
	}
}

func useMockMongo(t *testing.T, collection string) *mtest.T {
	t.Helper()

	mt := mtest.New(t, mtest.NewOptions().
		ClientType(mtest.Mock).
		ShareClient(true).
		DatabaseName("test").
		CollectionName(collection))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}
