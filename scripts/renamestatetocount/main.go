// Command renamestatetocount canonicalizes the legacy "state" BSON field into
// the "county" field in existing profile documents. Recognized values are
// normalized against counties.Normalize; unrecognized values remain untouched.
//
// This is a manual migration and is not invoked by deploy.yml.
//
// Usage:
//
//	go run ./scripts/renamestatetocount            # dry run, reports the plan only
//	go run ./scripts/renamestatetocount -write     # applies canonical updates
//
// Prerequisite: run -write only after the county-writing application version is
// stable and all old state-writing tasks have drained.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"backend/configs"
	"backend/counties"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type migrationCandidate struct {
	ID    any `bson:"_id"`
	State any `bson:"state"`
}

type normalizedCandidate struct {
	ID              any
	ObservedState   any
	CanonicalCounty string
}

type migrationCursor interface {
	Next(context.Context) bool
	Decode(any) error
	Err() error
	Close(context.Context) error
}

type migrationCollection struct {
	find      func(context.Context, bson.M) (migrationCursor, error)
	updateOne func(context.Context, bson.M, bson.M) (*mongo.UpdateResult, error)
}

type migrationClient interface {
	Disconnect(context.Context) error
}

type migrationDependencies struct {
	collection migrationCollection
	client     migrationClient
	output     io.Writer
}

func writeMigrationMessage(output io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(output, format+"\n", args...); err != nil {
		return fmt.Errorf("writing state-to-county migration output: %w", err)
	}
	return nil
}

func main() {
	write := flag.Bool("write", false, "apply canonical county updates (default is dry-run)")
	flag.Parse()

	if err := run(*write); err != nil {
		log.Fatal(err)
	}
}

func run(write bool) error {
	configs.ConnectDB()
	coll := configs.MI.DB.Collection("profiles")

	return runMigration(context.Background(), write, migrationDependencies{
		collection: migrationCollection{
			find: func(ctx context.Context, filter bson.M) (migrationCursor, error) {
				return coll.Find(ctx, filter, options.Find().SetProjection(bson.M{
					"_id":   1,
					"state": 1,
				}))
			},
			updateOne: func(ctx context.Context, filter, update bson.M) (*mongo.UpdateResult, error) {
				return coll.UpdateOne(ctx, filter, update)
			},
		},
		client: configs.MI.Client,
		output: os.Stdout,
	})
}

func runMigration(ctx context.Context, write bool, deps migrationDependencies) (err error) {
	defer func() {
		if disconnectErr := deps.client.Disconnect(ctx); disconnectErr != nil {
			err = errors.Join(err, fmt.Errorf("disconnecting from MongoDB: %w", disconnectErr))
		}
	}()

	cursor, err := deps.collection.find(ctx, migrationFilter())
	if err != nil {
		return fmt.Errorf("loading documents eligible for state-to-county migration: %w", err)
	}
	defer func() {
		if closeErr := cursor.Close(ctx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing state-to-county migration cursor: %w", closeErr))
		}
	}()

	recognized := make([]normalizedCandidate, 0)
	unrecognized := 0
	for cursor.Next(ctx) {
		var candidate migrationCandidate
		if err := cursor.Decode(&candidate); err != nil {
			return fmt.Errorf("decoding state-to-county migration candidate: %w", err)
		}

		state, isString := candidate.State.(string)
		canonical, isRecognized := counties.Normalize(state)
		if !isString || !isRecognized {
			unrecognized++
			continue
		}

		recognized = append(recognized, normalizedCandidate{
			ID:              candidate.ID,
			ObservedState:   candidate.State,
			CanonicalCounty: canonical,
		})
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("iterating state-to-county migration candidates: %w", err)
	}

	if err := writeMigrationMessage(deps.output, "Recognized candidates: %d.", len(recognized)); err != nil {
		return err
	}
	if err := writeMigrationMessage(deps.output, "Unrecognized candidates left unchanged: %d.", unrecognized); err != nil {
		return err
	}
	if err := writeMigrationMessage(deps.output, "Profiles that already have a 'county' field are skipped."); err != nil {
		return err
	}

	if len(recognized) == 0 {
		return writeMigrationMessage(deps.output, "Nothing to do.")
	}

	if !write {
		return writeMigrationMessage(deps.output, "DRY RUN: no changes written. Pass -write to apply.")
	}

	var matched, modified int64
	for _, candidate := range recognized {
		result, err := deps.collection.updateOne(ctx, candidateUpdateFilter(candidate), bson.M{
			"$set":   bson.M{"county": candidate.CanonicalCounty},
			"$unset": bson.M{"state": ""},
		})
		if err != nil {
			return fmt.Errorf("updating recognized state-to-county migration candidate: %w", err)
		}
		matched += result.MatchedCount
		modified += result.ModifiedCount
	}

	return writeMigrationMessage(
		deps.output,
		"WROTE: matched %d recognized candidate(s); modified %d.",
		matched,
		modified,
	)
}

func migrationFilter() bson.M {
	return bson.M{
		"state":  bson.M{"$exists": true},
		"county": bson.M{"$exists": false},
	}
}

func candidateUpdateFilter(candidate normalizedCandidate) bson.M {
	return bson.M{
		"_id":    candidate.ID,
		"state":  candidate.ObservedState,
		"county": bson.M{"$exists": false},
	}
}
