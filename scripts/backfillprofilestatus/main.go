// Command backfillprofilestatus is a one-time backfill for the new
// users.ProfileStatus field (see backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md).
//
// Rules:
//   - Every artisan with a currently-complete profile (per
//     profiles.GetProfileCompletion) is grandfathered in as "approved" —
//     this feature must not retroactively block artisans already using the
//     platform, including those already mid-assessment-journey or already
//     certified.
//   - Every other artisan (incomplete profile, including one with no
//     profiles-collection row at all) is set to "not_submitted".
//   - Only touches users whose profile_status is currently empty, so
//     running this twice is safe and does not clobber a status set by real
//     usage after the first run.
//
// Usage:
//
//	go run ./scripts/backfillprofilestatus            # dry run, prints counts only
//	go run ./scripts/backfillprofilestatus -write      # actually writes
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"backend/configs"
	"backend/profiles"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

// ComputeStatus is the pure decision rule, extracted for unit testing
// without a database.
func ComputeStatus(complete bool) string {
	if complete {
		return users.ProfileStatusApproved
	}
	return users.ProfileStatusNotSubmitted
}

func main() {
	write := flag.Bool("write", false, "actually write changes (default is dry-run)")
	flag.Parse()

	configs.ConnectDB()
	ctx := context.Background()

	cursor, err := configs.MI.DB.Collection("users").Find(ctx, bson.M{
		"user_type":      "artisan",
		"profile_status": bson.M{"$in": bson.A{"", nil}},
	})
	if err != nil {
		log.Fatalf("querying artisans: %v", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var toApprove, toNotSubmitted int
	for cursor.Next(ctx) {
		var u users.User
		if err := cursor.Decode(&u); err != nil {
			log.Printf("decoding user: %v", err)
			continue
		}
		profile, err := profiles.GetFullProfile(ctx, u.ID)
		if err != nil {
			log.Printf("loading profile for user %s: %v", u.ID, err)
			continue
		}
		status := ComputeStatus(profiles.GetProfileCompletion(profile).Complete)
		if status == users.ProfileStatusApproved {
			toApprove++
		} else {
			toNotSubmitted++
		}
		if *write {
			if err := users.UpdateUser(u.ID, bson.M{"profile_status": status}); err != nil {
				log.Printf("updating user %s: %v", u.ID, err)
			}
		}
	}

	mode := "DRY RUN"
	if *write {
		mode = "WROTE"
	}
	fmt.Printf("%s: %d users -> approved, %d users -> not_submitted\n", mode, toApprove, toNotSubmitted)
}
