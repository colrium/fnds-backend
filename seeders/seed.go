package seeders

import (
	"context"
	"log"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

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
	if empty, err := collectionEmpty("subscription_plans"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] subscription_plans already populated, skipping")
		return nil
	}
	plans := []interface{}{
		bson.M{
			"_id":         uuid.New().String(),
			"name":        "Basic",
			"description": "Basic subscription plan for artisans",
			"price":       0,
			"duration":    30,
			"is_default":  true,
			"created_at":  time.Now(),
			"updated_at":  time.Now(),
		},
		bson.M{
			"_id":         uuid.New().String(),
			"name":        "Premium",
			"description": "Premium subscription plan for artisans",
			"price":       5000,
			"duration":    30,
			"is_default":  false,
			"created_at":  time.Now(),
			"updated_at":  time.Now(),
		},
	}

	_, err := configs.MI.DB.Collection("subscription_plans").InsertMany(context.TODO(), plans)
	return err
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
	locations := make([]interface{}, 0, len(names))
	for _, n := range names {
		locations = append(locations, bson.M{"_id": uuid.New().String(), "name": n, "created_at": time.Now()})
	}
	_, err := configs.MI.DB.Collection("locations").InsertMany(context.TODO(), locations)
	return err
}

func SeedSkills() error {
	if empty, err := collectionEmpty("skills"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] skills already populated, skipping")
		return nil
	}
	names := []string{
		"Mason", "Carpenter", "Electrician", "Plumber", "Painter",
		"Welder", "Tile Fixer", "Gardener", "Cleaner", "General Labourer",
	}
	skills := make([]interface{}, 0, len(names))
	for _, n := range names {
		skills = append(skills, bson.M{"_id": uuid.New().String(), "name": n, "created_at": time.Now()})
	}
	_, err := configs.MI.DB.Collection("skills").InsertMany(context.TODO(), skills)
	return err
}

func SeedBanks() error {
	if empty, err := collectionEmpty("banks"); err != nil {
		return err
	} else if !empty {
		log.Printf("[seeder] banks already populated, skipping")
		return nil
	}
	banks := []interface{}{
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
	type inst struct {
		name string
		typ  string
	}
	items := []inst{
		{"National Industrial Training Authority (NITA)", "government"},
		{"Kenya Technical Trainers College", "college"},
		{"Nairobi Technical Training Institute", "tvet"},
		{"Kabete National Polytechnic", "polytechnic"},
		{"Kenya Coast National Polytechnic", "polytechnic"},
		{"Rift Valley Technical Training Institute", "tvet"},
		{"Kisumu National Polytechnic", "polytechnic"},
		{"Eldoret National Polytechnic", "polytechnic"},
	}
	institutions := make([]interface{}, 0, len(items))
	for _, it := range items {
		institutions = append(institutions, bson.M{
			"_id":        uuid.New().String(),
			"name":       it.name,
			"type":       it.typ,
			"created_at": time.Now(),
			"updated_at": time.Now(),
		})
	}
	_, err := configs.MI.DB.Collection("institutions").InsertMany(context.TODO(), institutions)
	return err
}
