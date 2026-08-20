package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeMigrationClient struct {
	disconnectCalls int
	disconnectErr   error
}

func (client *fakeMigrationClient) Disconnect(context.Context) error {
	client.disconnectCalls++
	return client.disconnectErr
}

type fakeMigrationCursor struct {
	candidates   []migrationCandidate
	decodeErr    error
	iterationErr error
	closeErr     error
	nextIndex    int
	current      int
	closeCalls   int
}

func (cursor *fakeMigrationCursor) Next(context.Context) bool {
	if cursor.nextIndex >= len(cursor.candidates) {
		return false
	}
	cursor.current = cursor.nextIndex
	cursor.nextIndex++
	return true
}

func (cursor *fakeMigrationCursor) Decode(value any) error {
	if cursor.decodeErr != nil {
		return cursor.decodeErr
	}
	candidate, ok := value.(*migrationCandidate)
	if !ok {
		return errors.New("unexpected decode target")
	}
	*candidate = cursor.candidates[cursor.current]
	return nil
}

func (cursor *fakeMigrationCursor) Err() error {
	return cursor.iterationErr
}

func (cursor *fakeMigrationCursor) Close(context.Context) error {
	cursor.closeCalls++
	return cursor.closeErr
}

type recordedUpdate struct {
	filter bson.M
	update bson.M
}

func TestMigrationFilterOnlySelectsSafeDocuments(t *testing.T) {
	t.Parallel()

	want := bson.M{
		"state":  bson.M{"$exists": true},
		"county": bson.M{"$exists": false},
	}
	if got := migrationFilter(); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrationFilter() = %#v, want %#v", got, want)
	}
}

func TestRunMigrationNormalizesRecognizedCandidatesAndPreservesUnrecognized(t *testing.T) {
	t.Parallel()

	cursor := &fakeMigrationCursor{candidates: []migrationCandidate{
		{ID: "nakuru-id", State: "Nakuru County"},
		{ID: "muranga-id", State: "  mUrAnGa   county  "},
		{ID: "unknown-id", State: "unknown place"},
	}}
	client := &fakeMigrationClient{}
	var output bytes.Buffer
	var findFilter bson.M
	var updates []recordedUpdate
	deps := migrationDependencies{
		collection: migrationCollection{
			find: func(_ context.Context, filter bson.M) (migrationCursor, error) {
				findFilter = filter
				return cursor, nil
			},
			updateOne: func(_ context.Context, filter, update bson.M) (*mongo.UpdateResult, error) {
				updates = append(updates, recordedUpdate{filter: filter, update: update})
				return &mongo.UpdateResult{MatchedCount: 1, ModifiedCount: 1}, nil
			},
		},
		client: client,
		output: &output,
	}

	if err := runMigration(context.Background(), true, deps); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}

	if !reflect.DeepEqual(findFilter, migrationFilter()) {
		t.Fatalf("find filter = %#v, want %#v", findFilter, migrationFilter())
	}
	wantUpdates := []recordedUpdate{
		{
			filter: bson.M{
				"_id":    "nakuru-id",
				"state":  "Nakuru County",
				"county": bson.M{"$exists": false},
			},
			update: bson.M{
				"$set":   bson.M{"county": "Nakuru"},
				"$unset": bson.M{"state": ""},
			},
		},
		{
			filter: bson.M{
				"_id":    "muranga-id",
				"state":  "  mUrAnGa   county  ",
				"county": bson.M{"$exists": false},
			},
			update: bson.M{
				"$set":   bson.M{"county": "Murang'a"},
				"$unset": bson.M{"state": ""},
			},
		},
	}
	if !reflect.DeepEqual(updates, wantUpdates) {
		t.Fatalf("updates = %#v, want %#v", updates, wantUpdates)
	}
	if client.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", client.disconnectCalls)
	}
	if cursor.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", cursor.closeCalls)
	}

	gotOutput := output.String()
	for _, want := range []string{
		"Recognized candidates: 2.",
		"Unrecognized candidates left unchanged: 1.",
		"WROTE: matched 2 recognized candidate(s); modified 2.",
	} {
		if !strings.Contains(gotOutput, want) {
			t.Errorf("output = %q, want it to contain %q", gotOutput, want)
		}
	}
	for _, forbidden := range []string{"Nakuru County", "Nakuru", "mUrAnGa", "Murang'a", "unknown place"} {
		if strings.Contains(gotOutput, forbidden) {
			t.Errorf("output = %q, must not contain location value %q", gotOutput, forbidden)
		}
	}
}

func TestRunMigrationDryRunReportsSamePlanWithoutWrites(t *testing.T) {
	t.Parallel()

	cursor := &fakeMigrationCursor{candidates: []migrationCandidate{
		{ID: "nakuru-id", State: "  NAKURU   COUNTY "},
		{ID: "nairobi-id", State: "Nairobi City"},
		{ID: "unknown-id", State: "unknown place"},
	}}
	client := &fakeMigrationClient{}
	var output bytes.Buffer
	updateCalls := 0
	deps := migrationDependencies{
		collection: migrationCollection{
			find: func(context.Context, bson.M) (migrationCursor, error) {
				return cursor, nil
			},
			updateOne: func(context.Context, bson.M, bson.M) (*mongo.UpdateResult, error) {
				updateCalls++
				return &mongo.UpdateResult{}, nil
			},
		},
		client: client,
		output: &output,
	}

	if err := runMigration(context.Background(), false, deps); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	if updateCalls != 0 {
		t.Fatalf("UpdateOne() calls = %d, want 0", updateCalls)
	}
	if client.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", client.disconnectCalls)
	}
	if cursor.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", cursor.closeCalls)
	}
	for _, want := range []string{
		"Recognized candidates: 2.",
		"Unrecognized candidates left unchanged: 1.",
		"DRY RUN: no changes written.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output = %q, want it to contain %q", output.String(), want)
		}
	}
}

func TestCandidateUpdateFilterChecksIdentityCurrentStateAndCountyAbsence(t *testing.T) {
	t.Parallel()

	candidate := normalizedCandidate{
		ID:              "profile-id",
		ObservedState:   "  NAKURU County ",
		CanonicalCounty: "Nakuru",
	}
	want := bson.M{
		"_id":    "profile-id",
		"state":  "  NAKURU County ",
		"county": bson.M{"$exists": false},
	}
	if got := candidateUpdateFilter(candidate); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidateUpdateFilter() = %#v, want %#v", got, want)
	}
}

func TestRunMigrationDisconnectsOnEveryExitPath(t *testing.T) {
	t.Parallel()

	findErr := errors.New("find failed")
	decodeErr := errors.New("decode failed")
	iterationErr := errors.New("iteration failed")
	updateErr := errors.New("update failed")
	tests := []struct {
		name            string
		write           bool
		candidates      []migrationCandidate
		findErr         error
		decodeErr       error
		iterationErr    error
		updateErr       error
		wantRunErr      error
		wantUpdateCalls int
	}{
		{name: "no recognized candidates", write: true},
		{
			name:       "dry run",
			candidates: []migrationCandidate{{ID: "id", State: "Nakuru"}},
		},
		{
			name:            "successful write",
			write:           true,
			candidates:      []migrationCandidate{{ID: "id", State: "Nakuru"}},
			wantUpdateCalls: 1,
		},
		{name: "find error", write: true, findErr: findErr, wantRunErr: findErr},
		{
			name:       "decode error",
			write:      true,
			candidates: []migrationCandidate{{ID: "id", State: "Nakuru"}},
			decodeErr:  decodeErr,
			wantRunErr: decodeErr,
		},
		{
			name:         "iteration error",
			write:        true,
			iterationErr: iterationErr,
			wantRunErr:   iterationErr,
		},
		{
			name:            "update error",
			write:           true,
			candidates:      []migrationCandidate{{ID: "id", State: "Nakuru"}},
			updateErr:       updateErr,
			wantRunErr:      updateErr,
			wantUpdateCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cursor := &fakeMigrationCursor{
				candidates:   test.candidates,
				decodeErr:    test.decodeErr,
				iterationErr: test.iterationErr,
			}
			client := &fakeMigrationClient{}
			updateCalls := 0
			deps := migrationDependencies{
				collection: migrationCollection{
					find: func(context.Context, bson.M) (migrationCursor, error) {
						if test.findErr != nil {
							return nil, test.findErr
						}
						return cursor, nil
					},
					updateOne: func(context.Context, bson.M, bson.M) (*mongo.UpdateResult, error) {
						updateCalls++
						return &mongo.UpdateResult{MatchedCount: 1, ModifiedCount: 1}, test.updateErr
					},
				},
				client: client,
				output: &bytes.Buffer{},
			}

			err := runMigration(context.Background(), test.write, deps)
			if test.wantRunErr == nil && err != nil {
				t.Fatalf("runMigration() error = %v, want nil", err)
			}
			if test.wantRunErr != nil && !errors.Is(err, test.wantRunErr) {
				t.Fatalf("runMigration() error = %v, want error wrapping %v", err, test.wantRunErr)
			}
			if client.disconnectCalls != 1 {
				t.Fatalf("Disconnect() calls = %d, want 1", client.disconnectCalls)
			}
			if updateCalls != test.wantUpdateCalls {
				t.Fatalf("UpdateOne() calls = %d, want %d", updateCalls, test.wantUpdateCalls)
			}
			wantCloseCalls := 1
			if test.findErr != nil {
				wantCloseCalls = 0
			}
			if cursor.closeCalls != wantCloseCalls {
				t.Fatalf("Close() calls = %d, want %d", cursor.closeCalls, wantCloseCalls)
			}
		})
	}
}

func TestRunMigrationSurfacesOperationAndCleanupErrors(t *testing.T) {
	t.Parallel()

	updateErr := errors.New("update failed")
	closeErr := errors.New("close failed")
	disconnectErr := errors.New("disconnect failed")
	cursor := &fakeMigrationCursor{
		candidates: []migrationCandidate{{ID: "id", State: "Nakuru"}},
		closeErr:   closeErr,
	}
	client := &fakeMigrationClient{disconnectErr: disconnectErr}
	deps := migrationDependencies{
		collection: migrationCollection{
			find: func(context.Context, bson.M) (migrationCursor, error) {
				return cursor, nil
			},
			updateOne: func(context.Context, bson.M, bson.M) (*mongo.UpdateResult, error) {
				return nil, updateErr
			},
		},
		client: client,
		output: &bytes.Buffer{},
	}

	err := runMigration(context.Background(), true, deps)
	for _, wantErr := range []error{updateErr, closeErr, disconnectErr} {
		if !errors.Is(err, wantErr) {
			t.Errorf("runMigration() error = %v, want it to include %v", err, wantErr)
		}
	}
	if cursor.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", cursor.closeCalls)
	}
	if client.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", client.disconnectCalls)
	}
}
