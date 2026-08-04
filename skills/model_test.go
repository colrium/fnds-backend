package skills

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGetSkillByID(t *testing.T) {
	t.Run("returns the matching skill", func(t *testing.T) {
		mt := useSkillsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			"test.skills",
			mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "skill-1"}, {Key: "name", Value: "Plumbing"}},
		))

		skill, err := GetSkillByID(context.Background(), "skill-1")
		if err != nil {
			t.Fatalf("GetSkillByID() error = %v", err)
		}
		if skill.ID != "skill-1" || skill.Name != "Plumbing" {
			t.Errorf("GetSkillByID() = %#v, want skill-1 Plumbing", skill)
		}

		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "find" {
			t.Fatalf("started event = %#v, want find", event)
		}
		var filter bson.M
		if err := bson.Unmarshal(event.Command.Lookup("filter").Document(), &filter); err != nil {
			t.Fatalf("decode find filter: %v", err)
		}
		if want := (bson.M{"_id": "skill-1"}); !reflect.DeepEqual(filter, want) {
			t.Errorf("find filter = %#v, want %#v", filter, want)
		}
	})

	t.Run("returns Mongo errors", func(t *testing.T) {
		mt := useSkillsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    2,
			Message: "bad value",
		}))

		skill, err := GetSkillByID(context.Background(), "skill-1")
		if err == nil || !strings.Contains(err.Error(), "bad value") {
			t.Fatalf("GetSkillByID() error = %v, want Mongo error", err)
		}
		if skill != nil {
			t.Errorf("GetSkillByID() skill = %#v, want nil", skill)
		}
	})
}

func TestGetSkillNamesByIDs(t *testing.T) {
	t.Run("empty IDs bypass Mongo and return a non-nil slice", func(t *testing.T) {
		names, err := GetSkillNamesByIDs(context.Background(), nil)
		if err != nil {
			t.Fatalf("GetSkillNamesByIDs() error = %v", err)
		}
		if names == nil || len(names) != 0 {
			t.Errorf("GetSkillNamesByIDs() = %#v, want empty non-nil slice", names)
		}
	})

	t.Run("preserves input order and skips unknown IDs", func(t *testing.T) {
		mt := useSkillsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			"test.skills",
			mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "skill-2"}, {Key: "name", Value: "Painting"}},
			bson.D{{Key: "_id", Value: "skill-1"}, {Key: "name", Value: "Plumbing"}},
		))

		names, err := GetSkillNamesByIDs(
			context.Background(),
			[]string{"skill-1", "unknown", "skill-2", "skill-1"},
		)
		if err != nil {
			t.Fatalf("GetSkillNamesByIDs() error = %v", err)
		}
		want := []string{"Plumbing", "Painting", "Plumbing"}
		if strings.Join(names, "|") != strings.Join(want, "|") {
			t.Errorf("GetSkillNamesByIDs() = %#v, want %#v", names, want)
		}
	})

	t.Run("returns cursor decode errors", func(t *testing.T) {
		mt := useSkillsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			"test.skills",
			mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "skill-1"}, {Key: "name", Value: int32(42)}},
		))

		names, err := GetSkillNamesByIDs(context.Background(), []string{"skill-1"})
		if err == nil {
			t.Fatal("GetSkillNamesByIDs() error = nil, want cursor decode error")
		}
		if !strings.Contains(err.Error(), "cannot decode") {
			t.Errorf("GetSkillNamesByIDs() error = %v, want cursor decode error", err)
		}
		if names != nil {
			t.Errorf("GetSkillNamesByIDs() names = %#v, want nil", names)
		}
	})

	t.Run("returns Mongo errors", func(t *testing.T) {
		mt := useSkillsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    2,
			Message: "bad value",
		}))

		names, err := GetSkillNamesByIDs(context.Background(), []string{"skill-1"})
		if err == nil || !strings.Contains(err.Error(), "bad value") {
			t.Fatalf("GetSkillNamesByIDs() error = %v, want Mongo error", err)
		}
		if names != nil {
			t.Errorf("GetSkillNamesByIDs() names = %#v, want nil", names)
		}
	})
}

func useSkillsMockMongo(t *testing.T) *mtest.T {
	t.Helper()

	mt := mtest.New(t, mtest.NewOptions().
		ClientType(mtest.Mock).
		ShareClient(true).
		DatabaseName("test").
		CollectionName("skills"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}
