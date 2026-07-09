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

func SeedSubscriptionPlans() error {
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
	locations := []interface{}{
		bson.M{"_id": uuid.New().String(), "name": "Kigali", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Butare", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Gisenyi", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Musanze", "created_at": time.Now()},
	}
	_, err := configs.MI.DB.Collection("locations").InsertMany(context.TODO(), locations)
	return err
}

func SeedSkills() error {
	skills := []interface{}{
		bson.M{"_id": uuid.New().String(), "name": "Plumbing", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Electrical", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Carpentry", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Painting", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Welding", "created_at": time.Now()},
	}
	_, err := configs.MI.DB.Collection("skills").InsertMany(context.TODO(), skills)
	return err
}

func SeedBanks() error {
	banks := []interface{}{
		bson.M{"_id": uuid.New().String(), "name": "Bank of Kigali", "code": "BK001", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "Equity Bank", "code": "EQ001", "created_at": time.Now()},
		bson.M{"_id": uuid.New().String(), "name": "I&M Bank", "code": "IM001", "created_at": time.Now()},
	}
	_, err := configs.MI.DB.Collection("banks").InsertMany(context.TODO(), banks)
	return err
}
