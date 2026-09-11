package adminapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"backend/configs"
	"backend/files"
	"backend/profiles"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type profileReviewDocument struct {
	files.File
	DocumentTypeName string `json:"document_type_name"`
	DownloadURL      string `json:"download_url,omitempty"`
	PreviewError     string `json:"preview_error,omitempty"`
}

type profileReviewDetail struct {
	*profiles.FullProfile
	SkillNames        map[string]string          `json:"skill_names"`
	LocationNames     map[string]string          `json:"location_names"`
	InstitutionName   string                     `json:"institution_name,omitempty"`
	ReviewedByName    string                     `json:"reviewed_by_name,omitempty"`
	Documents         []profileReviewDocument    `json:"documents"`
	ProfileCompletion profiles.ProfileCompletion `json:"profile_completion"`
}

// Keep IDs in the original profile fields, while providing labels for review.
// References can select a different skill from the artisan's own trade.
func loadProfileReviewDetail(ctx context.Context, userID string) (*profileReviewDetail, error) {
	profile, err := profiles.GetFullProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	detail := &profileReviewDetail{
		FullProfile:       profile,
		ProfileCompletion: profiles.GetProfileCompletion(profile),
	}
	skillIDs := append([]string{}, profile.Skills...)
	for _, reference := range profile.References {
		skillIDs = append(skillIDs, reference.JobTypeID)
	}
	detail.SkillNames, err = profileReviewNames(ctx, "skills", skillIDs)
	if err != nil {
		return nil, err
	}
	locationIDs := append([]string{}, profile.Locations...)
	for _, employer := range profile.PastEmployers {
		locationIDs = append(locationIDs, employer.Location)
	}
	for _, reference := range profile.References {
		locationIDs = append(locationIDs, reference.Location)
	}
	detail.LocationNames, err = profileReviewNames(ctx, "locations", locationIDs)
	if err != nil {
		return nil, err
	}
	institutions, err := profileReviewNames(ctx, "institutions", []string{profile.Institution})
	if err != nil {
		return nil, err
	}
	detail.InstitutionName = institutions[profile.Institution]
	detail.Documents, err = loadProfileReviewDocuments(ctx, userID)
	if err != nil {
		return nil, err
	}
	if profile.ProfileReviewedBy != "" {
		reviewer, err := users.GetUserByID(ctx, profile.ProfileReviewedBy)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, err
		}
		if reviewer != nil {
			detail.ReviewedByName = strings.TrimSpace(reviewer.FirstName + " " + reviewer.LastName)
			if detail.ReviewedByName == "" {
				detail.ReviewedByName = reviewer.Email
			}
		}
	}
	return detail, nil
}

func profileReviewNames(ctx context.Context, collection string, ids []string) (map[string]string, error) {
	names := make(map[string]string)
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		if id != "" && !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	if len(unique) == 0 {
		return names, nil
	}
	cursor, err := configs.MI.DB.Collection(collection).Find(ctx,
		bson.M{"_id": bson.M{"$in": unique}},
		options.Find().SetProjection(bson.M{"_id": 1, "name": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("loading profile review %s: %w", collection, err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	var rows []struct {
		ID   string `bson:"_id"`
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

func loadProfileReviewDocuments(ctx context.Context, userID string) ([]profileReviewDocument, error) {
	if artisanFileRepository == nil || artisanFileStorage == nil {
		return nil, errors.New("profile review file storage is not configured")
	}
	stored, err := artisanFileRepository.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	typeIDs := make([]string, 0, len(stored))
	for _, file := range stored {
		typeIDs = append(typeIDs, file.DocumentTypeID)
	}
	typeNames, err := profileReviewNames(ctx, "document_types", typeIDs)
	if err != nil {
		return nil, err
	}
	// CV is a reserved document type and does not require a document_types row.
	typeNames[files.CVDocumentTypeID] = "CV"
	sort.SliceStable(stored, func(i, j int) bool {
		if stored[i].CreatedAt.Equal(stored[j].CreatedAt) {
			return stored[i].ID < stored[j].ID
		}
		return stored[i].CreatedAt.After(stored[j].CreatedAt)
	})
	documents := make([]profileReviewDocument, 0, len(stored))
	for _, file := range stored {
		document := profileReviewDocument{File: file, DocumentTypeName: typeNames[file.DocumentTypeID]}
		if document.DocumentTypeName == "" {
			document.DocumentTypeName = "Other document"
		}
		key, err := artisanFileStorage.KeyFromURL(file.FileURL)
		if err != nil || file.UserID != userID || !strings.HasPrefix(key, "users/"+userID+"/") {
			document.PreviewError = "This file's stored location could not be verified."
		} else if downloadURL, err := artisanFileStorage.PresignDownload(ctx, key); err != nil {
			document.PreviewError = "This file could not be opened. Refresh files to try again."
		} else {
			document.DownloadURL = downloadURL
		}
		// A bad legacy file must remain visible to the reviewer, without hiding
		// the rest of the submission or presigning a different user's object.
		documents = append(documents, document)
	}
	return documents, nil
}
