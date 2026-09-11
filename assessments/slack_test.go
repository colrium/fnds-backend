package assessments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/integrations"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestAssessmentNotificationResolvesAllRecordNames(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("artisan assessor reviewer and skill", func(mt *mtest.T) {
		useStartJourneyMockMongo(mt)
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "u1"}, {Key: "first_name", Value: "Jane"}},
				bson.D{{Key: "_id", Value: "a1"}, {Key: "first_name", Value: "Peter"}},
				bson.D{{Key: "_id", Value: "r1"}, {Key: "first_name", Value: "Sam"}},
			),
			mtest.CreateCursorResponse(0, "test.skills", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "s1"}, {Key: "name", Value: "Plumbing"}}),
		)
		payload := make(chan integrations.BlockKitMessage, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var msg integrations.BlockKitMessage
			_ = json.NewDecoder(r.Body).Decode(&msg)
			payload <- msg
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		previous := opsSlackClient
		opsSlackClient = integrations.NewSlackActivityClient(server.URL)
		mt.Cleanup(func() { opsSlackClient = previous })
		mt.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")
		mt.Setenv("APP_ENV", "production")
		notifyOps(context.Background(), integrations.AssessmentEvent{
			Title: "Assessment booked", UserID: "u1", AssessorID: "a1", ActorID: "r1", JourneyID: "j1",
		}, "s1")
		msg := <-payload
		for _, want := range []string{"/users/u1|Jane>", "/users/a1|Peter>", "/users/r1|Sam>", "/assessments/journeys/j1|Plumbing>"} {
			if !strings.Contains(msg.Text, want) {
				mt.Errorf("notification missing %s: %s", want, msg.Text)
			}
		}
	})
}
