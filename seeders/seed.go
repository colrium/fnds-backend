package seeders

import (
	"context"
	"log"
	"time"

	"backend/configs"
	"backend/subscriptions"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
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
	skills := make([]any, 0, len(names))
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
	type inst struct {
		name string
		typ  string
	}
	// Comprehensive list of Kenyan education institutions. Types are limited to
	// "university", "college", "tvet" and "polytechnic".
	items := []inst{
		// Universities
		{"University of Nairobi", "university"},
		{"Kenyatta University", "university"},
		{"Jomo Kenyatta University of Agriculture and Technology", "university"},
		{"Moi University", "university"},
		{"Egerton University", "university"},
		{"Maseno University", "university"},
		{"Technical University of Kenya", "university"},
		{"Technical University of Mombasa", "university"},
		{"Dedan Kimathi University of Technology", "university"},
		{"Masinde Muliro University of Science and Technology", "university"},
		{"Multimedia University of Kenya", "university"},
		{"Meru University of Science and Technology", "university"},
		{"Murang'a University of Technology", "university"},
		{"Machakos University", "university"},
		{"Kirinyaga University", "university"},
		{"Kibabii University", "university"},
		{"Karatina University", "university"},
		{"Laikipia University", "university"},
		{"Chuka University", "university"},
		{"Pwani University", "university"},
		{"South Eastern Kenya University", "university"},
		{"Taita Taveta University", "university"},
		{"University of Embu", "university"},
		{"University of Eldoret", "university"},
		{"University of Kabianga", "university"},
		{"Kisii University", "university"},
		{"Rongo University", "university"},
		{"Jaramogi Oginga Odinga University of Science and Technology", "university"},
		{"Tom Mboya University", "university"},
		{"Alupe University", "university"},
		{"Garissa University", "university"},
		{"Cooperative University of Kenya", "university"},
		{"Strathmore University", "university"},
		{"United States International University Africa", "university"},
		{"Mount Kenya University", "university"},
		{"Kenya Methodist University", "university"},
		{"Catholic University of Eastern Africa", "university"},
		{"Daystar University", "university"},
		{"Africa Nazarene University", "university"},
		{"KCA University", "university"},
		{"Zetech University", "university"},
		{"Riara University", "university"},

		// Colleges
		{"Kenya Technical Trainers College", "college"},
		{"Kenya Institute of Highways and Building Technology", "college"},
		{"Kenya Water Institute", "college"},
		{"Kenya School of Government", "college"},
		{"Kenya Institute of Mass Communication", "college"},
		{"Kenya Medical Training College", "college"},
		{"Kenya Utalii College", "college"},
		{"Kenya Institute of Management", "college"},
		{"Railway Training Institute", "college"},
		{"Kenya Wildlife Service Training Institute", "college"},
		{"Kenya Forestry College", "college"},
		{"Kenya School of Law", "college"},
		{"Bandari Maritime Academy", "college"},
		{"East African Institute of Certified Studies", "college"},
		{"Nairobi Institute of Business Studies", "college"},
		{"Graffins College", "college"},
		{"Regina Pacis Institute", "college"},
		{"Don Bosco Technical Institute Embu", "college"},
		{"St. Joseph Technical Institute for the Deaf Nyang'oma", "college"},
		{"Christ the King Technical Institute", "college"},

		// TVETs
		// NITA is classified as a TVET here (it was previously "government",
		// which is not one of the supported institution types listed above).
		// NITA runs technical/vocational training centres, so "tvet" is the
		// closest fit within the allowed vocabulary.
		{"National Industrial Training Authority (NITA)", "tvet"},
		{"Nairobi Technical Training Institute", "tvet"},
		{"Rift Valley Technical Training Institute", "tvet"},
		{"Thika Technical Training Institute", "tvet"},
		{"Kiambu Institute of Science and Technology", "tvet"},
		{"Machakos Institute of Technology", "tvet"},
		{"Michuki Technical Training Institute", "tvet"},
		{"Kaiboi Technical Training Institute", "tvet"},
		{"Sang'alo Institute of Science and Technology", "tvet"},
		{"Bushiangala Technical Training Institute", "tvet"},
		{"Mawego Technical Training Institute", "tvet"},
		{"Ramogi Institute of Advanced Technology", "tvet"},
		{"Nyandarua Institute of Science and Technology", "tvet"},
		{"Rift Valley Institute of Science and Technology", "tvet"},
		{"Masai Technical Training Institute", "tvet"},
		{"Coast Institute of Technology", "tvet"},
		{"Mombasa Technical Training Institute", "tvet"},
		{"Wote Technical Training Institute", "tvet"},
		{"Kabete Technical Training Institute", "tvet"},
		{"Karen Technical Training Institute for the Deaf", "tvet"},
		{"Nairobi Institute of Technology", "tvet"},
		{"Gusii Institute of Technology", "tvet"},
		{"Kisiwa Technical Training Institute", "tvet"},
		{"Bumbe Technical Training Institute", "tvet"},
		{"Ol'lessos Technical Training Institute", "tvet"},
		{"Kipsoen Technical Training Institute", "tvet"},
		{"Baringo Technical College", "tvet"},
		{"Emining Technical Training Institute", "tvet"},
		{"Keroka Technical Training Institute", "tvet"},
		{"Nkabune Technical Training Institute", "tvet"},
		{"Kitui Technical Training Institute", "tvet"},
		{"Garissa Technical Training Institute", "tvet"},
		{"Godoma Technical Training Institute", "tvet"},

		// Polytechnics
		{"Kabete National Polytechnic", "polytechnic"},
		{"Kenya Coast National Polytechnic", "polytechnic"},
		{"Kisumu National Polytechnic", "polytechnic"},
		{"Eldoret National Polytechnic", "polytechnic"},
		{"Meru National Polytechnic", "polytechnic"},
		{"Nyeri National Polytechnic", "polytechnic"},
		{"Sigalagala National Polytechnic", "polytechnic"},
		{"Kitale National Polytechnic", "polytechnic"},
		{"Kirinyaga National Polytechnic", "polytechnic"},
		{"Nyandarua National Polytechnic", "polytechnic"},
		{"Bungoma National Polytechnic", "polytechnic"},
		{"Kabarnet National Polytechnic", "polytechnic"},
		{"Kisii National Polytechnic", "polytechnic"},
		{"Machakos National Polytechnic", "polytechnic"},
		{"Thika National Polytechnic", "polytechnic"},
	}
	institutions := make([]any, 0, len(items))
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
