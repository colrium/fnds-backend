package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/configs"
	"backend/integrations"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestAdminUserNamesBatchAndFallback(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("display fields only, duplicate IDs, email fallback", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
		mt.Cleanup(func() { configs.MI = previous })
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "u1"}, {Key: "first_name", Value: " Jane "}, {Key: "last_name", Value: "Doe"}},
			bson.D{{Key: "_id", Value: "u2"}, {Key: "email", Value: "alex@example.com"}},
		))
		got := AdminUserNames(context.Background(), "u1", "u2", "u1", "", "missing")
		if got["u1"] != "Jane Doe" || got["u2"] != "alex@example.com" || got["missing"] != "" {
			mt.Fatalf("unexpected display names: %#v", got)
		}
		events := mt.GetAllStartedEvents()
		if len(events) != 1 {
			mt.Fatalf("expected one batch lookup, got %d", len(events))
		}
		command := events[0].Command
		ids, err := command.Lookup("filter", "_id", "$in").Array().Values()
		if err != nil || len(ids) != 3 {
			mt.Fatal("lookup did not deduplicate or remove empty IDs")
		}
		projection, _ := command.Lookup("projection").Document().Elements()
		if len(projection) != 3 {
			mt.Fatal("only first name, last name, and email should be read")
		}
	})
}

func TestVacancyNotificationEnrichmentFailureStillDelivers(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("lookup fails", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
		mt.Cleanup(func() { configs.MI = previous })
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "lookup unavailable"}))
		payload := make(chan integrations.BlockKitMessage, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var msg integrations.BlockKitMessage
			_ = json.NewDecoder(r.Body).Decode(&msg)
			payload <- msg
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		mt.Setenv("SLACK_OPS_NOTIFS_URL", server.URL)
		mt.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")
		if err := NotifyVacancyCreated(context.Background(), VacancyCreatedActivity{
			VacancyID: "v1", VacancyTitle: "Plumber", CreatorUserID: "u1",
		}); err != nil {
			mt.Fatal(err)
		}
		msg := <-payload
		if !strings.Contains(msg.Text, "/users/u1|View account>") {
			mt.Fatalf("fallback must remain readable and linked: %s", msg.Text)
		}
	})
}
