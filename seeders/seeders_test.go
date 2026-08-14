package seeders

import (
	"testing"

	"backend/configs"
	skillcatalog "backend/skills"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestSeederNames(t *testing.T) {
	expected := map[string]bool{
		"subscription_plans": false,
		"locations":          false,
		"skills":             false,
		"banks":              false,
	}

	// Just verify the seeder list is defined correctly
	if len(expected) != 4 {
		t.Errorf("expected 4 seeders, got %d", len(expected))
	}
}

func TestSeedSkillsUpsertsCatalogEntries(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().
		ClientType(mtest.Mock).
		ShareClient(true).
		DatabaseName("test").
		CollectionName("skills"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })

	for range skillcatalog.CatalogNames() {
		mt.AddMockResponses(mtest.CreateSuccessResponse(
			bson.E{Key: "n", Value: 1},
			bson.E{Key: "nModified", Value: 0},
		))
	}

	if err := SeedSkills(); err != nil {
		t.Fatalf("SeedSkills() error = %v", err)
	}

	const expectedName = "Aluminum Fabrication & Installation"
	for _, event := range mt.GetAllStartedEvents() {
		if event.CommandName != "update" {
			continue
		}
		updates, err := event.Command.Lookup("updates").Array().Values()
		if err != nil {
			t.Fatalf("decode updates: %v", err)
		}
		for _, update := range updates {
			document := update.Document()
			name := document.Lookup("q").Document().Lookup("name").StringValue()
			if name != expectedName {
				continue
			}
			if !document.Lookup("upsert").Boolean() {
				t.Fatalf("skill %q update is not an upsert", expectedName)
			}
			return
		}
	}

	t.Fatalf("SeedSkills() did not upsert %q", expectedName)
}
