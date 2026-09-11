package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/files"
	"backend/jwtutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

type reviewFileRepository struct {
	files.Repository
	list   []files.File
	userID string
	err    error
}

func (r *reviewFileRepository) ListByUserID(_ context.Context, userID string) ([]files.File, error) {
	r.userID = userID
	return r.list, r.err
}

type reviewFileStorage struct {
	files.Storage
	signed []string
}

func (s *reviewFileStorage) KeyFromURL(raw string) (string, error) {
	if !strings.HasPrefix(raw, "https://storage.test/") {
		return "", errors.New("unrecognized storage URL")
	}
	return strings.TrimPrefix(raw, "https://storage.test/"), nil
}

func (s *reviewFileStorage) PresignDownload(_ context.Context, key string) (string, error) {
	s.signed = append(s.signed, key)
	if strings.HasSuffix(key, "unavailable.pdf") {
		return "", errors.New("storage unavailable")
	}
	return "https://storage.test/" + key + "?signed=true", nil
}

func useReviewFiles(t *testing.T, list []files.File) (*reviewFileRepository, *reviewFileStorage) {
	t.Helper()
	previousRepository, previousStorage := artisanFileRepository, artisanFileStorage
	t.Cleanup(func() {
		SetArtisanFileDependencies(previousRepository, previousStorage)
	})
	repository := &reviewFileRepository{list: list}
	storage := &reviewFileStorage{}
	SetArtisanFileDependencies(repository, storage)
	return repository, storage
}

func queueReviewProfile(mt *mtest.T) {
	mt.AddMockResponses(
		mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "artisan-1"},
			{Key: "user_type", Value: "artisan"},
			{Key: "first_name", Value: "Amina"},
			{Key: "last_name", Value: "Otieno"},
			{Key: "email", Value: "amina@example.test"},
			{Key: "avatar_id", Value: "https://storage.test/users/artisan-1/avatars/photo.png"},
			{Key: "profile_status", Value: "pending_review"},
			{Key: "password", Value: "must-not-leak"},
		}),
		mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "profile-1"}, {Key: "user_id", Value: "artisan-1"},
			{Key: "id_number", Value: "ID123"}, {Key: "date_of_birth", Value: "1990-04-20"},
			{Key: "gender", Value: "female"}, {Key: "address", Value: "Nairobi"},
			{Key: "skills", Value: bson.A{"skill-1"}},
			{Key: "skill_acquisition_mode", Value: "apprenticeship"},
			{Key: "locations", Value: bson.A{"location-1", "deleted-location"}},
			{Key: "institution", Value: "institution-1"},
			{Key: "next_of_kin_name", Value: "Mary"}, {Key: "next_of_kin_phone_number", Value: "0700000000"},
			{Key: "next_of_kin_relationship", Value: "sister"},
		}),
		mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, bson.D{
			{Key: "day", Value: "monday"}, {Key: "opening_time", Value: "08:00"}, {Key: "closing_time", Value: "17:00"},
		}),
		mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch, bson.D{
			{Key: "name", Value: "Workshop"}, {Key: "location", Value: "location-1"},
		}),
		mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch, bson.D{
			{Key: "language", Value: "Swahili"}, {Key: "proficiency_level", Value: "fluent"},
		}),
		mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, bson.D{
			{Key: "name", Value: "John"}, {Key: "phone_number", Value: "0711111111"},
			{Key: "email", Value: "john@example.test"}, {Key: "job_title", Value: "Supervisor"},
			{Key: "job_type_id", Value: "reference-skill"},
		}),
		mtest.CreateCursorResponse(0, "test.skills", mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "skill-1"}, {Key: "name", Value: "Carpentry"}},
			bson.D{{Key: "_id", Value: "reference-skill"}, {Key: "name", Value: "Joinery"}},
		),
		mtest.CreateCursorResponse(0, "test.locations", mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "location-1"}, {Key: "name", Value: "Nairobi"}},
		),
		mtest.CreateCursorResponse(0, "test.institutions", mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "institution-1"}, {Key: "name", Value: "Technical College"}},
		),
	)
}

func TestProfileReviewReturnsFullIdentityLabelsAndDocuments(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	t.Setenv("API_PUBLIC_URL", "https://api.test")
	queueReviewProfile(mt)
	now := time.Now().UTC()
	repository, _ := useReviewFiles(t, []files.File{
		{ID: "cv", UserID: "artisan-1", DocumentTypeID: files.CVDocumentTypeID, FileName: "CV.pdf", FileURL: "https://storage.test/users/artisan-1/cv.pdf", MimeType: "application/pdf", Size: 2000, CreatedAt: now},
		{ID: "photo", UserID: "artisan-1", DocumentTypeID: "work-photo", FileName: "Kitchen.png", FileURL: "https://storage.test/users/artisan-1/kitchen.png", MimeType: "image/png", Size: 1000, CreatedAt: now.Add(-time.Hour)},
	})
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.document_types", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "work-photo"}, {Key: "name", Value: "Work photos"}},
	))
	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews/artisan-1", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("artisan-1")
	if err := HandleGetProfileReview(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data profileReviewDetail `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	detail := body.Data
	if detail.User == nil || detail.ID != "artisan-1" || detail.FirstName != "Amina" || detail.IDNumber != "ID123" {
		t.Fatalf("profile identity or submitted fields missing: %+v", detail.FullProfile)
	}
	if !strings.HasPrefix(detail.Avatar, "https://api.test/api/v1/avatars/artisan-1") {
		t.Fatalf("avatar = %q", detail.Avatar)
	}
	if detail.SkillNames["skill-1"] != "Carpentry" || detail.SkillNames["reference-skill"] != "Joinery" ||
		detail.LocationNames["location-1"] != "Nairobi" || detail.InstitutionName != "Technical College" {
		t.Fatalf("missing lookup labels: %+v", detail)
	}
	if len(detail.Locations) != 2 || detail.Locations[1] != "deleted-location" {
		t.Fatal("unresolved submitted selections must be preserved")
	}
	if len(detail.References) != 1 || detail.References[0].Email != "john@example.test" || len(detail.PastEmployers) != 1 || len(detail.LanguageProficiencies) != 1 || len(detail.WorkingHours) != 1 {
		t.Fatal("submitted history/reference details missing")
	}
	if !detail.ProfileCompletion.Complete || len(detail.Documents) != 2 || repository.userID != "artisan-1" {
		t.Fatalf("completion/documents missing or unscoped: %+v", detail)
	}
	if detail.Documents[0].DocumentTypeName != "CV" || detail.Documents[1].DocumentTypeName != "Work photos" ||
		detail.Documents[1].DownloadURL != "https://storage.test/users/artisan-1/kitchen.png?signed=true" {
		t.Fatalf("document metadata/URLs = %+v", detail.Documents)
	}
	if strings.Contains(rec.Body.String(), "must-not-leak") || strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatal("password must not be included in the admin review")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private review responses must not be cached")
	}
}

func TestReviewDocumentsPreserveUnavailableFilesWithoutSigningOtherUsersObjects(t *testing.T) {
	useAdminMockMongo(t, "users")
	_, storage := useReviewFiles(t, []files.File{
		{ID: "valid", UserID: "artisan-1", FileURL: "https://storage.test/users/artisan-1/photo.png"},
		{ID: "wrong-prefix", UserID: "artisan-1", FileURL: "https://storage.test/users/artisan-12/private.pdf"},
		{ID: "wrong-owner", UserID: "artisan-2", FileURL: "https://storage.test/users/artisan-1/private.pdf"},
		{ID: "legacy", UserID: "artisan-1", FileURL: "https://legacy.test/document.pdf"},
		{ID: "sign-error", UserID: "artisan-1", FileURL: "https://storage.test/users/artisan-1/unavailable.pdf"},
	})
	documents, err := loadProfileReviewDocuments(context.Background(), "artisan-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 5 || len(storage.signed) != 2 {
		t.Fatalf("documents=%d signed=%v", len(documents), storage.signed)
	}
	for _, doc := range documents {
		if doc.ID == "valid" {
			if doc.DownloadURL == "" || doc.PreviewError != "" {
				t.Fatal("valid document was not available")
			}
		} else if doc.DownloadURL != "" || doc.PreviewError == "" {
			t.Fatalf("unavailable document must remain visible without a URL: %+v", doc)
		}
	}
}

func TestReviewDocumentsListFailureIsNotAnEmptySubmission(t *testing.T) {
	repository, _ := useReviewFiles(t, nil)
	repository.err = errors.New("database unavailable")
	if _, err := loadProfileReviewDocuments(context.Background(), "artisan-1"); err == nil {
		t.Fatal("file listing errors must not look like no uploaded documents")
	}
}

func TestReviewDocumentsEmptyListIsJSONArray(t *testing.T) {
	useReviewFiles(t, nil)
	documents, err := loadProfileReviewDocuments(context.Background(), "artisan-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(documents)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("empty documents = %s, err=%v", encoded, err)
	}
}

func TestAdminGroupRejectsNonAdminProfileReviewDetail(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "artisan-1", "artisan")
	queueUserLookup(mt, "artisan-1", "artisan")
	app := buildAdminGroup()
	token, _, err := jwtutil.GenerateToken("artisan-1")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/profile-reviews/artisan-2", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin status=%d, want 401", rec.Code)
	}
}
