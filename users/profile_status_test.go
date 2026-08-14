package users

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestCreateNewUserDefaultsProfileStatusToNotSubmitted(t *testing.T) {
	mt := useMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateSuccessResponse())

	user := &User{FirstName: "Jane", LastName: "Doe"}
	if err := CreateNewUser(user); err != nil {
		mt.Fatalf("CreateNewUser() error = %v", err)
	}
	if user.ProfileStatus != ProfileStatusNotSubmitted {
		t.Errorf("ProfileStatus = %q, want %q", user.ProfileStatus, ProfileStatusNotSubmitted)
	}

	event := mt.GetStartedEvent()
	if event == nil || event.CommandName != "insert" {
		t.Fatalf("started event = %#v, want insert", event)
	}
	var command struct {
		Documents []bson.M `bson:"documents"`
	}
	if err := bson.Unmarshal(event.Command, &command); err != nil {
		t.Fatalf("decode insert command: %v", err)
	}
	if len(command.Documents) != 1 || command.Documents[0]["profile_status"] != ProfileStatusNotSubmitted {
		t.Errorf("inserted document = %#v, want profile_status %q", command.Documents, ProfileStatusNotSubmitted)
	}
}

func TestCreateNewUserPreservesExplicitProfileStatus(t *testing.T) {
	mt := useMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateSuccessResponse())

	user := &User{FirstName: "Jane", LastName: "Doe", ProfileStatus: ProfileStatusApproved}
	if err := CreateNewUser(user); err != nil {
		mt.Fatalf("CreateNewUser() error = %v", err)
	}
	if user.ProfileStatus != ProfileStatusApproved {
		t.Errorf("ProfileStatus = %q, want %q (explicit value must be preserved)", user.ProfileStatus, ProfileStatusApproved)
	}
}

func TestEffectiveProfileStatusDefaultsEmptyToNotSubmitted(t *testing.T) {
	u := &User{ProfileStatus: ""}
	if got := EffectiveProfileStatus(u); got != ProfileStatusNotSubmitted {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusNotSubmitted)
	}
}

func TestEffectiveProfileStatusPassesThroughSetValue(t *testing.T) {
	u := &User{ProfileStatus: ProfileStatusApproved}
	if got := EffectiveProfileStatus(u); got != ProfileStatusApproved {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusApproved)
	}
}
