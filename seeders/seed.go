package seeders

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"backend/configs"
	"backend/files"
	skillcatalog "backend/skills"
	"backend/subscriptions"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// InstitutionsDataPath is the location of the institutions seed file. Ops can
// override with the INSTITUTIONS_SEED_PATH env var so the list can be updated
// without a code change.
const defaultInstitutionsDataPath = "seeders/data/institutions.json"

type Seeder struct {
	Name string
	Run  func() error
}

func RunAll() {
	seeders := []Seeder{
		{Name: "subscription_plans", Run: SeedSubscriptionPlans},
		{Name: "locations", Run: SeedLocations},
		{Name: "skills", Run: SeedSkills},
		{Name: "banks", Run: SeedBanks},
		{Name: "institutions", Run: SeedInstitutions},
		{Name: "document_types", Run: SeedDocumentTypes},
	}

	for _, s := range seeders {
		log.Printf("[seeder] running %s...", s.Name)
		if err := s.Run(); err != nil {
			log.Printf("[seeder] %s failed: %v", s.Name, err)
		} else {
			log.Printf("[seeder] %s completed", s.Name)
		}
	}
}

// collectionEmpty reports whether a collection has no documents. Seeders skip
// when their collection is already populated so RunAll() is safe to call on
// every startup without duplicating rows.
func collectionEmpty(name string) (bool, error) {
	count, err := configs.MI.DB.Collection(name).EstimatedDocumentCount(context.TODO())
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func SeedSubscriptionPlans() error {
	ctx := context.TODO()
	for _, plan := range subscriptions.Plans() {
		_, err := configs.MI.DB.Collection("subscription_plans").UpdateOne(
			ctx,
			bson.M{"_id": plan.ID},
			bson.M{
				"$set": bson.M{
					"name":         plan.Name,
					"description":  plan.Description,
					"audience":     plan.Audience,
					"price":        plan.PriceKES,
					"currency":     plan.Currency,
					"duration":     plan.DurationDays,
					"features":     plan.Features,
					"entitlements": plan.Entitlements,
					"is_default":   plan.IsDefault,
					"updated_at":   time.Now().UTC(),
				},
				"$setOnInsert": bson.M{"created_at": time.Now().UTC()},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func SeedLocations() error {
	if empty, err := collectionEmpty("locations"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] locations already populated, skipping")
		return nil
	}
	names := []string{
		"Nairobi", "Mombasa", "Kisumu", "Nakuru", "Eldoret",
		"Thika", "Nyeri", "Machakos", "Kakamega", "Kisii",
		"Meru", "Kericho", "Naivasha", "Kitale", "Malindi",
	}
	locations := make([]any, 0, len(names))
	for _, n := range names {
		locations = append(locations, bson.M{"_id": uuid.New().String(), "name": n, "created_at": time.Now()})
	}
	_, err := configs.MI.DB.Collection("locations").InsertMany(context.TODO(), locations)
	return err
}

func SeedSkills() error {
	ctx := context.TODO()
	for _, name := range skillcatalog.CatalogNames() {
		_, err := configs.MI.DB.Collection("skills").UpdateOne(
			ctx,
			bson.M{"name": name},
			bson.M{
				"$setOnInsert": bson.M{
					"_id":        uuid.New().String(),
					"name":       name,
					"created_at": time.Now().UTC(),
				},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("upsert skill %q: %w", name, err)
		}
	}
	return nil
}

func SeedBanks() error {
	if empty, err := collectionEmpty("banks"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] banks already populated, skipping")
		return nil
	}
	banks := []any{
		bson.M{"_id": uuid.New().String(), "name": "Kenya Commercial Bank", "code": "KCB", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Equity Bank", "code": "EQUITY", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Co-operative Bank", "code": "COOP", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Absa Bank Kenya", "code": "ABSA", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Standard Chartered Kenya", "code": "SCB", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "NCBA Bank", "code": "NCBA", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "DTB Kenya", "code": "DTB", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "I&M Bank", "code": "IM", "created_at": time.Now()},
	}
	_, err := configs.MI.DB.Collection("banks").InsertMany(context.TODO(), banks)
	return err
}

func SeedInstitutions() error {
	if empty, err := collectionEmpty("institutions"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] institutions already populated, skipping")
		return nil
	}
	items, err := loadInstitutionsData()
	if err != nil {
		return fmt.Errorf("load institutions data: %w", err)
	}
	institutions := make([]any, 0, len(items))
	for _, it := range items {
		institutions = append(institutions, bson.M{
			"_id":        uuid.New().String(),
			"name":       it.Name,
			"type":       it.Type,
			"created_at": time.Now(),
			"updated_at": time.Now(),
		})
	}
	_, err = configs.MI.DB.Collection("institutions").InsertMany(context.TODO(), institutions)
	return err
}

type institutionSeed struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func loadInstitutionsData() ([]institutionSeed, error) {
	path := os.Getenv("INSTITUTIONS_SEED_PATH")
	if path == "" {
		path = defaultInstitutionsDataPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var items []institutionSeed
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return items, nil
}

// SeedDocumentTypes upserts well-known document type records into the
// document_types collection. Currently seeds only the CV document type.
func SeedDocumentTypes() error {
	ctx := context.TODO()

	type docTypeSeed struct {
		ID   string
		Name string
	}

	types := []docTypeSeed{
		{ID: files.CVDocumentTypeID, Name: "CV"},
	}

	for _, dt := range types {
		_, err := configs.MI.DB.Collection("document_types").UpdateOne(
			ctx,
			bson.M{"_id": dt.ID},
			bson.M{
				"$set": bson.M{
					"name":       dt.Name,
					"updated_at": time.Now().UTC(),
				},
				"$setOnInsert": bson.M{"created_at": time.Now().UTC()},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("upsert document type %q: %w", dt.Name, err)
		}
	}
	return nil
}
